package python

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/cloudcompiler/cloudcc/internal/diag"
	"github.com/cloudcompiler/cloudcc/internal/source"
)

func rewritten(t *testing.T, src string) string {
	t.Helper()
	f := &source.File{Path: "app.py", Content: []byte(src)}
	if err := f.ParsePython(); err != nil {
		t.Fatal(err)
	}
	var d diag.Diagnostics
	hints := detectHints(f, &d)
	if d.HasErrors() {
		t.Fatalf("detection errors: %v", d.Items())
	}
	if err := rewrite(f, hints); err != nil {
		t.Fatal(err)
	}
	out := string(f.Content)
	assertParses(t, out)
	return out
}

// assertParses runs the rewritten source through the real interpreter's
// parser. A rewrite that produces syntactically invalid Python is the failure
// mode that matters most here, and tree-sitter is too forgiving to catch it.
func assertParses(t *testing.T, src string) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not on PATH")
	}
	cmd := exec.Command(python, "-c", "import ast,sys; ast.parse(sys.stdin.read())")
	cmd.Stdin = strings.NewReader(src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("rewritten source is not valid Python: %v\n%s\n--- source ---\n%s", err, out, src)
	}
}

func TestRewritePersistKV(t *testing.T) {
	got := rewritten(t, "import cloudcompiler as cloudcc\npets = cloudcc.persist(boto3.resource(\"dynamodb\").Table(\"t\"), id=\"petsByOwner\")\n")

	if strings.Contains(got, "cloudcompiler") {
		t.Errorf("the SDK import should be removed:\n%s", got)
	}
	if !strings.Contains(got, `pets = _cloudcc_kv.connect("petsByOwner")`) {
		t.Errorf("call was not rewritten:\n%s", got)
	}
	if !strings.Contains(got, "from _cloudcc_runtime import kv as _cloudcc_kv") {
		t.Errorf("shim import was not injected:\n%s", got)
	}
}

func TestRewriteLeavesApplicationCodeAlone(t *testing.T) {
	src := `from fastapi import FastAPI
import cloudcompiler as cloudcc

app = FastAPI()
pets = cloudcc.persist(boto3.resource("dynamodb").Table("t"), id="petsByOwner")


@app.get("/pets/{pet_id}")
def get_pet(pet_id: str):
    return pets.get(pet_id)
`
	got := rewritten(t, src)
	for _, want := range []string{
		"from fastapi import FastAPI",
		"app = FastAPI()",
		`@app.get("/pets/{pet_id}")`,
		"def get_pet(pet_id: str):",
		"return pets.get(pet_id)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rewrite lost %q:\n%s", want, got)
		}
	}
}

func TestRewriteExpose(t *testing.T) {
	got := rewritten(t, "from fastapi import FastAPI\nimport cloudcompiler as cloudcc\napp = FastAPI()\ncloudcc.expose(app, id=\"pet-api\")\n")
	if !strings.Contains(got, `_cloudcc_expose.register(app, id="pet-api")`) {
		t.Errorf("expose was not rewritten:\n%s", got)
	}
}

func TestRewriteConfigValue(t *testing.T) {
	got := rewritten(t, "import cloudcompiler as cloudcc\nlvl = cloudcc.config_value(\"log_level\", default=\"info\")\n")
	if !strings.Contains(got, `_cloudcc_config.value("log_level", default="info")`) {
		t.Errorf("config_value was not rewritten:\n%s", got)
	}
}

func TestCompileOnlyHintsAreErased(t *testing.T) {
	got := rewritten(t, `import cloudcompiler as cloudcc

cloudcc.execution_unit(id="api")
cloudcc.static_unit("site", static_files="./public/**/*")
`)
	for _, gone := range []string{"execution_unit", "static_unit"} {
		if strings.Contains(got, gone) {
			t.Errorf("%s should be erased from the compiled copy:\n%s", gone, got)
		}
	}
	if strings.Count(got, "None") != 2 {
		t.Errorf("expected both hints to become None:\n%s", got)
	}
	// Nothing was imported for them, because they have no runtime behaviour.
	if strings.Contains(got, "_cloudcc_runtime") {
		t.Errorf("compile-only hints should not pull in a shim:\n%s", got)
	}
}

func TestEmbedAssetsKeepsItsPattern(t *testing.T) {
	got := rewritten(t, "import cloudcompiler as cloudcc\np = cloudcc.embed_assets(\"./data/*.json\")\n")
	if !strings.Contains(got, `p = "./data/*.json"`) {
		t.Errorf("embed_assets should collapse to its pattern:\n%s", got)
	}
}

func TestImportsGoAfterTheDocstring(t *testing.T) {
	got := rewritten(t, `"""Module docstring."""

import cloudcompiler as cloudcc

pets = cloudcc.persist(boto3.resource("dynamodb").Table("t"), id="a")
`)
	if !strings.HasPrefix(got, `"""Module docstring."""`) {
		t.Errorf("the docstring must stay first:\n%s", got)
	}
	docEnd := strings.Index(got, `"""`+"\n") // closing quotes of the docstring
	importAt := strings.Index(got, "from _cloudcc_runtime")
	if importAt < docEnd {
		t.Errorf("shim import was placed before the docstring:\n%s", got)
	}
}

func TestFutureImportsStayFirst(t *testing.T) {
	got := rewritten(t, `from __future__ import annotations

import cloudcompiler as cloudcc

pets = cloudcc.persist(boto3.resource("dynamodb").Table("t"), id="a")
`)
	future := strings.Index(got, "from __future__ import annotations")
	shim := strings.Index(got, "from _cloudcc_runtime")
	if future < 0 || shim < future {
		t.Errorf("__future__ import must remain first:\n%s", got)
	}
}

func TestFromImportFormIsRewritten(t *testing.T) {
	got := rewritten(t, "from cloudcompiler import persist, Topic\npets = persist(Topic(), id=\"a\")\n")
	if !strings.Contains(got, `pets = _cloudcc_pubsub.connect("a")`) || strings.Contains(got, "cloudcompiler") {
		t.Errorf("from-import form was not rewritten:\n%s", got)
	}
}

func TestSeveralHintsInOneFile(t *testing.T) {
	got := rewritten(t, `import cloudcompiler as cloudcc

pets = cloudcc.persist(boto3.resource("dynamodb").Table("t"), id="pets")
blobs = cloudcc.persist(Path("./data"), id="blobs")
events = cloudcc.persist(cloudcc.Topic(), id="events")
level = cloudcc.config_value("log_level")
`)
	for _, want := range []string{
		`pets = _cloudcc_kv.connect("pets")`,
		`blobs = _cloudcc_fs.connect("blobs")`,
		`events = _cloudcc_pubsub.connect("events")`,
		`level = _cloudcc_config.value("log_level")`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	// Imports are emitted in sorted order, so the output is deterministic.
	want := "from _cloudcc_runtime import config as _cloudcc_config\n" +
		"from _cloudcc_runtime import fs as _cloudcc_fs\n" +
		"from _cloudcc_runtime import kv as _cloudcc_kv\n" +
		"from _cloudcc_runtime import pubsub as _cloudcc_pubsub\n"
	if !strings.Contains(got, want) {
		t.Errorf("import block is not sorted:\n%s", got)
	}
}

func TestRewriteIsDeterministic(t *testing.T) {
	src := "import cloudcompiler as cloudcc\na = cloudcc.persist(boto3.resource(\"dynamodb\").Table(\"t\"), id=\"a\")\nb = cloudcc.persist(Path(\"./data\"), id=\"b\")\nc = cloudcc.persist(cloudcc.Topic(), id=\"c\")\n"
	first := rewritten(t, src)
	for i := 0; i < 10; i++ {
		if got := rewritten(t, src); got != first {
			t.Fatalf("run %d differs:\n%s\n---\n%s", i, first, got)
		}
	}
}

func TestFileWithoutSDKIsUntouched(t *testing.T) {
	src := "import os\n\ndef f():\n    return os.getcwd()\n"
	f := &source.File{Path: "a.py", Content: []byte(src)}
	if err := f.ParsePython(); err != nil {
		t.Fatal(err)
	}
	if err := rewrite(f, nil); err != nil {
		t.Fatal(err)
	}
	if string(f.Content) != src {
		t.Errorf("a file with no SDK usage was modified:\n%s", f.Content)
	}
}

func TestUnusedSDKImportIsStripped(t *testing.T) {
	src := "import cloudcompiler as cloudcc\n\n\ndef noop() -> None:\n    return None\n"
	f := &source.File{Path: "helpers.py", Content: []byte(src)}
	if err := f.ParsePython(); err != nil {
		t.Fatal(err)
	}
	if err := rewrite(f, nil); err != nil {
		t.Fatal(err)
	}
	got := string(f.Content)
	if strings.Contains(got, "cloudcompiler") {
		t.Errorf("an unused SDK import survived:\n%s", got)
	}
	if !strings.Contains(got, "def noop()") {
		t.Errorf("the rest of the module was lost:\n%s", got)
	}
	assertParses(t, got)
}

func TestUnusedFromImportIsStripped(t *testing.T) {
	src := "from cloudcompiler import persist\n\nVALUE = 1\n"
	f := &source.File{Path: "helpers.py", Content: []byte(src)}
	if err := f.ParsePython(); err != nil {
		t.Fatal(err)
	}
	if err := rewrite(f, nil); err != nil {
		t.Fatal(err)
	}
	if got := string(f.Content); strings.Contains(got, "cloudcompiler") {
		t.Errorf("an unused from-import survived:\n%s", got)
	}
}
