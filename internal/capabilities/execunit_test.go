package capabilities

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cloudcompiler/cloudcc/internal/compiler"
	"github.com/cloudcompiler/cloudcc/internal/config"
	"github.com/cloudcompiler/cloudcc/internal/ir"
	"github.com/spf13/afero"
)

// harness compiles an in-memory source tree through a plugin chain and returns
// the resulting context.
func harness(t *testing.T, files map[string]string, extra ...compiler.Plugin) *compiler.Context {
	t.Helper()
	return harnessWithConfig(t, files, "", extra...)
}

// harnessWithConfig is harness plus a cloudcc.yaml, for the cases where what is
// being tested is how configuration and code interact.
func harnessWithConfig(t *testing.T, files map[string]string, yaml string, extra ...compiler.Plugin) *compiler.Context {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.New()
	cfg.App = "test"
	if yaml != "" {
		path := filepath.Join(root, "cloudcc.yaml")
		if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
			t.Fatal(err)
		}
		loaded, err := config.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		cfg = loaded
	}
	ctx := compiler.NewContext(cfg, root, afero.NewMemMapFs())
	t.Cleanup(func() { ctx.Files.Close() })

	c, err := compiler.NewCompiler(Chain(extra...))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Compile(ctx); err != nil {
		t.Fatalf("compile failed: %v\ndiagnostics: %v", err, ctx.Diags.Items())
	}
	return ctx
}

func diagStrings(ctx *compiler.Context) []string {
	var out []string
	for _, d := range ctx.Diags.Items() {
		out = append(out, d.String())
	}
	return out
}

func TestNoHintsGivesOneUnitNamedMain(t *testing.T) {
	ctx := harness(t, map[string]string{
		"app.py":    "print('hi')\n",
		"helper.py": "def f(): pass\n",
	})
	if got := config.SortedKeys(ctx.UnitFiles); !reflect.DeepEqual(got, []string{DefaultUnitID}) {
		t.Fatalf("units = %v, want [main]", got)
	}
	// helper.py is not imported, so it is pruned with a warning rather than
	// silently bundled.
	if got := ctx.UnitFiles[DefaultUnitID]; !reflect.DeepEqual(got, []string{"app.py"}) {
		t.Errorf("main files = %v", got)
	}
	if !containsSubstr(diagStrings(ctx), "no execution unit imports this file") {
		t.Errorf("expected an unreachable-file warning, got %v", diagStrings(ctx))
	}
}

func TestClosureFollowsLocalImports(t *testing.T) {
	ctx := harness(t, map[string]string{
		"app.py":          "import os\nfrom pkg.helper import f\nimport fastapi\n",
		"pkg/__init__.py": "",
		"pkg/helper.py":   "from .deep import g\ndef f(): return g()\n",
		"pkg/deep.py":     "def g(): return 1\n",
		"unused.py":       "x = 1\n",
	})
	want := []string{"app.py", "pkg/__init__.py", "pkg/deep.py", "pkg/helper.py"}
	if got := ctx.UnitFiles[DefaultUnitID]; !reflect.DeepEqual(got, want) {
		t.Errorf("files = %v, want %v", got, want)
	}
}

func TestExternalImportsAreNotWarnings(t *testing.T) {
	ctx := harness(t, map[string]string{
		"app.py": "import os\nimport fastapi\nfrom boto3 import client\n",
	})
	if len(ctx.Diags.Items()) != 0 {
		t.Errorf("third-party imports should be silent, got %v", diagStrings(ctx))
	}
}

func TestBrokenRelativeImportWarns(t *testing.T) {
	ctx := harness(t, map[string]string{
		"app.py": "from .missing import thing\n",
	})
	if !containsSubstr(diagStrings(ctx), "could not be resolved") {
		t.Errorf("expected an unresolved relative-import warning, got %v", diagStrings(ctx))
	}
}

func TestMultipleUnitsShareAFile(t *testing.T) {
	ctx := harness(t, map[string]string{
		"api.py":             "import cloudcompiler as cloudcc\ncloudcc.execution_unit(id=\"api\")\nfrom shared.store import pets\n",
		"worker.py":          "import cloudcompiler as cloudcc\ncloudcc.execution_unit(id=\"worker\")\nfrom shared.store import pets\n",
		"shared/__init__.py": "",
		"shared/store.py":    "import cloudcompiler as cloudcc\npets = cloudcc.persist(boto3.resource(\"dynamodb\").Table(\"t\"), id=\"petsByOwner\")\n",
	})
	units := config.SortedKeys(ctx.UnitFiles)
	if !reflect.DeepEqual(units, []string{"api", "worker"}) {
		t.Fatalf("units = %v", units)
	}
	for _, unit := range units {
		if !contains(ctx.UnitFiles[unit], "shared/store.py") {
			t.Errorf("unit %q is missing the shared module: %v", unit, ctx.UnitFiles[unit])
		}
	}
	if contains(ctx.UnitFiles["api"], "worker.py") {
		t.Error("api unit swallowed worker.py")
	}
	if contains(ctx.UnitFiles["worker"], "api.py") {
		t.Error("worker unit swallowed api.py")
	}
	if got := ctx.UnitsFor("shared/store.py"); !reflect.DeepEqual(got, []string{"api", "worker"}) {
		t.Errorf("UnitsFor = %v", got)
	}
}

func TestExecutionUnitInsideFunctionIsAnError(t *testing.T) {
	root := t.TempDir()
	write(t, root, "app.py", "import cloudcompiler as cloudcc\ndef f():\n    cloudcc.execution_unit(id=\"api\")\n")
	ctx := compileExpectingDiags(t, root)
	if !containsSubstr(diagStrings(ctx), "must be called at module level") {
		t.Errorf("diagnostics = %v", diagStrings(ctx))
	}
}

func TestTwoUnitsCannotShareAnEntrypoint(t *testing.T) {
	root := t.TempDir()
	write(t, root, "app.py",
		"import cloudcompiler as cloudcc\ncloudcc.execution_unit(id=\"a\")\ncloudcc.execution_unit(id=\"b\")\n")
	ctx := compileExpectingDiags(t, root)
	if !containsSubstr(diagStrings(ctx), "already the entrypoint") {
		t.Errorf("diagnostics = %v", diagStrings(ctx))
	}
}

func TestNonPythonFilesTravelWithEveryUnit(t *testing.T) {
	ctx := harness(t, map[string]string{
		"api.py":           "import cloudcompiler as cloudcc\ncloudcc.execution_unit(id=\"api\")\n",
		"worker.py":        "import cloudcompiler as cloudcc\ncloudcc.execution_unit(id=\"worker\")\n",
		"requirements.txt": "fastapi\n",
		"templates/a.html": "<p></p>",
	})
	for _, unit := range []string{"api", "worker"} {
		for _, want := range []string{"requirements.txt", "templates/a.html"} {
			if !contains(ctx.UnitFiles[unit], want) {
				t.Errorf("unit %q is missing %s: %v", unit, want, ctx.UnitFiles[unit])
			}
		}
	}
}

func TestStaticUnitClaimsFilesBeforeClosure(t *testing.T) {
	ctx := harness(t, map[string]string{
		"app.py": "import cloudcompiler as cloudcc\n" +
			"cloudcc.static_unit(\"site\", static_files=\"./public/**/*\")\n",
		"public/index.html": "<html></html>",
		"public/app.js":     "console.log(1)",
		"data/keep.json":    "{}",
	})
	if got, want := ctx.ClaimedFiles["public/index.html"], "site"; got != want {
		t.Errorf("claim = %q, want %q", got, want)
	}
	files := ctx.UnitFiles[DefaultUnitID]
	for _, claimed := range []string{"public/index.html", "public/app.js"} {
		if contains(files, claimed) {
			t.Errorf("%s leaked into the compute bundle: %v", claimed, files)
		}
	}
	if !contains(files, "data/keep.json") {
		t.Errorf("unclaimed asset was dropped: %v", files)
	}

	sites := ctx.Graph.IntentsOfKind(config.KindStaticUnit)
	if len(sites) != 1 {
		t.Fatalf("got %d static sites", len(sites))
	}
	site := sites[0].(*ir.StaticSite)
	if !reflect.DeepEqual(site.Files, []string{"public/app.js", "public/index.html"}) {
		t.Errorf("site files = %v", site.Files)
	}
	if site.IndexDocument != "index.html" {
		t.Errorf("index document = %q", site.IndexDocument)
	}
}

func TestSharedFilesStayAvailableToUnits(t *testing.T) {
	ctx := harness(t, map[string]string{
		"app.py": "import cloudcompiler as cloudcc\n" +
			"cloudcc.static_unit(\"site\", static_files=\"./public/*.html\", shared_files=\"./public/shared.json\")\n",
		"public/index.html":  "<html></html>",
		"public/shared.json": "{}",
	})
	if contains(ctx.UnitFiles[DefaultUnitID], "public/index.html") {
		t.Error("static_files should be claimed out of the compute bundle")
	}
	if !contains(ctx.UnitFiles[DefaultUnitID], "public/shared.json") {
		t.Errorf("shared_files should stay usable by units: %v", ctx.UnitFiles[DefaultUnitID])
	}
	site := ctx.Graph.IntentsOfKind(config.KindStaticUnit)[0].(*ir.StaticSite)
	if !contains(site.Files, "public/shared.json") {
		t.Errorf("shared_files should still be uploaded: %v", site.Files)
	}
}

func TestUnitFilesWrittenToSeparateOutputDirectories(t *testing.T) {
	ctx := harness(t, map[string]string{
		"api.py":    "import cloudcompiler as cloudcc\ncloudcc.execution_unit(id=\"api\")\n",
		"worker.py": "import cloudcompiler as cloudcc\ncloudcc.execution_unit(id=\"worker\")\n",
	})

	for path, wantSubstr := range map[string]string{
		"api/api.py":                  "None",
		"worker/worker.py":            "None",
		"api/_cloudcc_runtime/kv.py":  "def connect(",
		"api/cloudcc_lambda_entry.py": "def handler(",
		"api/requirements.txt":        "boto3",
	} {
		data, err := afero.ReadFile(ctx.Out, path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		if !strings.Contains(string(data), wantSubstr) {
			t.Errorf("%s = %q", path, data)
		}
	}
	if ok, _ := afero.Exists(ctx.Out, "api/worker.py"); ok {
		t.Error("worker.py was copied into the api bundle")
	}
}

func TestStaticAssetsWrittenUnderTheirSiteRoot(t *testing.T) {
	ctx := harness(t, map[string]string{
		"app.py": "import cloudcompiler as cloudcc\n" +
			"cloudcc.static_unit(\"site\", static_files=\"./public/**/*\")\n",
		"public/index.html":  "<html></html>",
		"public/css/app.css": "body{}",
	})

	for _, want := range []string{"static/site/index.html", "static/site/css/app.css"} {
		if ok, _ := afero.Exists(ctx.Out, want); !ok {
			t.Errorf("expected %s in the output", want)
		}
	}
}

func TestSourceTreeIsNeverModified(t *testing.T) {
	root := t.TempDir()
	src := "import cloudcompiler as cloudcc\npets = cloudcc.persist(boto3.resource(\"dynamodb\").Table(\"t\"), id=\"petsByOwner\")\n"
	write(t, root, "app.py", src)

	cfg := config.New()
	cfg.App = "test"
	ctx := compiler.NewContext(cfg, root, afero.NewMemMapFs())
	defer ctx.Files.Close()
	c, err := compiler.NewCompiler(Chain())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Compile(ctx); err != nil {
		t.Fatal(err)
	}

	after, err := os.ReadFile(filepath.Join(root, "app.py"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != src {
		t.Errorf("the compiler modified the user's source:\n%s", after)
	}
}

// compileExpectingDiags runs only the intent stages, so a program with
// diagnostics still produces a context to assert against rather than failing
// downstream in the resolver.
func compileExpectingDiags(t *testing.T, root string) *compiler.Context {
	return compileWith(t, root, IntentChain())
}

func compileWith(t *testing.T, root string, plugins []compiler.Plugin) *compiler.Context {
	t.Helper()
	cfg := config.New()
	cfg.App = "test"
	ctx := compiler.NewContext(cfg, root, afero.NewMemMapFs())
	t.Cleanup(func() { ctx.Files.Close() })
	c, err := compiler.NewCompiler(plugins)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Compile(ctx); err != nil {
		t.Fatalf("compile returned a hard error: %v", err)
	}
	return ctx
}

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func containsSubstr(s []string, sub string) bool {
	for _, x := range s {
		if strings.Contains(x, sub) {
			return true
		}
	}
	return false
}

// A package's __init__.py is often empty and often the shallowest Python file
// in the tree. Picking it as the entrypoint produces a unit containing nothing
// at all, which is how this was found: by a generated program whose modules
// lived under a package directory.
func TestDefaultEntrypointPrefersTheExposedModule(t *testing.T) {
	ctx := harness(t, map[string]string{
		"src/__init__.py":     "",
		"src/svc/__init__.py": "",
		"src/svc/api.py": "from fastapi import FastAPI\nimport cloudcompiler as cloudcc\n" +
			"from .store import pets\napp = FastAPI()\ncloudcc.expose(app, id=\"gw\")\n",
		"src/svc/store.py": "import cloudcompiler as cloudcc\npets = cloudcc.persist(boto3.resource(\"dynamodb\").Table(\"t\"), id=\"pets\")\n",
	})

	unit := ctx.Graph.IntentsOfKind(config.KindExecutionUnit)[0].(*ir.ExecUnit)
	if unit.Entrypoint() != "src/svc/api.py" {
		t.Errorf("entrypoint = %q, want the module that exposes the app", unit.Entrypoint())
	}
	for _, want := range []string{"src/svc/api.py", "src/svc/store.py"} {
		if !contains(ctx.UnitFiles[unit.ID], want) {
			t.Errorf("unit is missing %s: %v", want, ctx.UnitFiles[unit.ID])
		}
	}
}

// With nothing exposed, an empty __init__.py must still not win.
func TestDefaultEntrypointSkipsEmptyModules(t *testing.T) {
	ctx := harness(t, map[string]string{
		"pkg/__init__.py": "\n\n",
		"pkg/worker.py":   "import cloudcompiler as cloudcc\nq = cloudcc.persist(boto3.resource(\"dynamodb\").Table(\"t\"), id=\"q\")\n",
	})
	unit := ctx.Graph.IntentsOfKind(config.KindExecutionUnit)[0].(*ir.ExecUnit)
	if unit.Entrypoint() != "pkg/worker.py" {
		t.Errorf("entrypoint = %q, want the module with something in it", unit.Entrypoint())
	}
}
