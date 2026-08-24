package py

import (
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/cloudcompiler/cc/internal/diag"
	"github.com/cloudcompiler/cc/internal/sdkdetect"
	"github.com/cloudcompiler/cc/internal/source"
)

func rewrite(t *testing.T, src string) string {
	t.Helper()
	f := &source.File{Path: "app.py", Content: []byte(src)}
	if err := f.ParsePython(); err != nil {
		t.Fatal(err)
	}
	var d diag.Diagnostics
	hints := sdkdetect.Detect(f, &d)
	if d.HasErrors() {
		t.Fatalf("detection errors: %v", d.Items())
	}
	if err := Rewrite(f, hints); err != nil {
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
	got := rewrite(t, "import cloudcompiler as cc\npets = cc.persist_kv(\"petsByOwner\")\n")

	if strings.Contains(got, "cloudcompiler") {
		t.Errorf("the SDK import should be removed:\n%s", got)
	}
	if !strings.Contains(got, `pets = _cc_kv.connect("petsByOwner")`) {
		t.Errorf("call was not rewritten:\n%s", got)
	}
	if !strings.Contains(got, "from _cc_runtime import kv as _cc_kv") {
		t.Errorf("shim import was not injected:\n%s", got)
	}
}

func TestRewriteLeavesApplicationCodeAlone(t *testing.T) {
	src := `from fastapi import FastAPI
import cloudcompiler as cc

app = FastAPI()
pets = cc.persist_kv("petsByOwner")


@app.get("/pets/{pet_id}")
def get_pet(pet_id: str):
    return pets.get(pet_id)
`
	got := rewrite(t, src)
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
	got := rewrite(t, "from fastapi import FastAPI\nimport cloudcompiler as cc\napp = FastAPI()\ncc.expose(app, id=\"pet-api\")\n")
	if !strings.Contains(got, `_cc_expose.register(app, id="pet-api")`) {
		t.Errorf("expose was not rewritten:\n%s", got)
	}
}

func TestRewriteConfigValue(t *testing.T) {
	got := rewrite(t, "import cloudcompiler as cc\nlvl = cc.config_value(\"log_level\", default=\"info\")\n")
	if !strings.Contains(got, `_cc_config.value("log_level", default="info")`) {
		t.Errorf("config_value was not rewritten:\n%s", got)
	}
}

func TestCompileOnlyHintsAreErased(t *testing.T) {
	got := rewrite(t, `import cloudcompiler as cc

cc.execution_unit(id="api")
cc.static_unit("site", static_files="./public/**/*")
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
	if strings.Contains(got, "_cc_runtime") {
		t.Errorf("compile-only hints should not pull in a shim:\n%s", got)
	}
}

func TestEmbedAssetsKeepsItsPattern(t *testing.T) {
	got := rewrite(t, "import cloudcompiler as cc\np = cc.embed_assets(\"./data/*.json\")\n")
	if !strings.Contains(got, `p = "./data/*.json"`) {
		t.Errorf("embed_assets should collapse to its pattern:\n%s", got)
	}
}

func TestImportsGoAfterTheDocstring(t *testing.T) {
	got := rewrite(t, `"""Module docstring."""

import cloudcompiler as cc

pets = cc.persist_kv("a")
`)
	if !strings.HasPrefix(got, `"""Module docstring."""`) {
		t.Errorf("the docstring must stay first:\n%s", got)
	}
	docEnd := strings.Index(got, `"""`+"\n") // closing quotes of the docstring
	importAt := strings.Index(got, "from _cc_runtime")
	if importAt < docEnd {
		t.Errorf("shim import was placed before the docstring:\n%s", got)
	}
}

func TestFutureImportsStayFirst(t *testing.T) {
	got := rewrite(t, `from __future__ import annotations

import cloudcompiler as cc

pets = cc.persist_kv("a")
`)
	future := strings.Index(got, "from __future__ import annotations")
	shim := strings.Index(got, "from _cc_runtime")
	if future < 0 || shim < future {
		t.Errorf("__future__ import must remain first:\n%s", got)
	}
}

func TestFromImportFormIsRewritten(t *testing.T) {
	got := rewrite(t, "from cloudcompiler import persist_kv\npets = persist_kv(\"a\")\n")
	if !strings.Contains(got, `pets = _cc_kv.connect("a")`) || strings.Contains(got, "cloudcompiler") {
		t.Errorf("from-import form was not rewritten:\n%s", got)
	}
}

func TestSeveralHintsInOneFile(t *testing.T) {
	got := rewrite(t, `import cloudcompiler as cc

pets = cc.persist_kv("pets")
blobs = cc.persist_fs("blobs")
events = cc.pubsub_topic("events")
level = cc.config_value("log_level")
`)
	for _, want := range []string{
		`pets = _cc_kv.connect("pets")`,
		`blobs = _cc_fs.connect("blobs")`,
		`events = _cc_pubsub.connect("events")`,
		`level = _cc_config.value("log_level")`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	// Imports are emitted in sorted order, so the output is deterministic.
	want := "from _cc_runtime import config as _cc_config\n" +
		"from _cc_runtime import fs as _cc_fs\n" +
		"from _cc_runtime import kv as _cc_kv\n" +
		"from _cc_runtime import pubsub as _cc_pubsub\n"
	if !strings.Contains(got, want) {
		t.Errorf("import block is not sorted:\n%s", got)
	}
}

func TestRewriteIsDeterministic(t *testing.T) {
	src := "import cloudcompiler as cc\na = cc.persist_kv(\"a\")\nb = cc.persist_fs(\"b\")\nc = cc.pubsub_topic(\"c\")\n"
	first := rewrite(t, src)
	for i := 0; i < 10; i++ {
		if got := rewrite(t, src); got != first {
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
	if err := Rewrite(f, nil); err != nil {
		t.Fatal(err)
	}
	if string(f.Content) != src {
		t.Errorf("a file with no SDK usage was modified:\n%s", f.Content)
	}
}

func TestRuntimeFilesAreEmbedded(t *testing.T) {
	files, err := RuntimeFiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"_cc_runtime/__init__.py",
		"_cc_runtime/_client.py",
		"_cc_runtime/kv.py",
		"_cc_runtime/fs.py",
		"_cc_runtime/secret.py",
		"_cc_runtime/orm.py",
		"_cc_runtime/redis_.py",
		"_cc_runtime/pubsub.py",
		"_cc_runtime/config.py",
		"_cc_runtime/expose.py",
	} {
		if _, ok := files[want]; !ok {
			t.Errorf("missing embedded file %s", want)
		}
	}
	for name, content := range files {
		assertParsesNamed(t, name, string(content))
	}
}

func assertParsesNamed(t *testing.T, name, src string) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not on PATH")
	}
	cmd := exec.Command(python, "-c", "import ast,sys; ast.parse(sys.stdin.read())")
	cmd.Stdin = strings.NewReader(src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%s is not valid Python: %v\n%s", name, err, out)
	}
}

func TestOnlyTheShimsImportBoto3(t *testing.T) {
	files, err := RuntimeFiles()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(files["_cc_runtime/_client.py"]), "import boto3") {
		t.Error("_client.py should be where boto3 lives")
	}
	for name, content := range files {
		if name == "_cc_runtime/_client.py" {
			continue
		}
		if strings.Contains(string(content), "import boto3") {
			t.Errorf("%s imports boto3 directly; it should go through _client", name)
		}
	}
}

func TestRenderLambdaEntry(t *testing.T) {
	got, err := RenderLambdaEntry(UnitTemplateData{Unit: "api", EntryModule: "app", ASGIApp: "app"})
	if err != nil {
		t.Fatal(err)
	}
	src := string(got)
	assertParses(t, src)
	for _, want := range []string{
		`importlib.import_module("app")`,
		"from mangum import Mangum",
		`Mangum(getattr(_module, "app")`,
		"def handler(event, context):",
		"_cc_pubsub.is_sns_event(event)",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("missing %q in:\n%s", want, src)
		}
	}
}

func TestRenderLambdaEntryWithoutAnASGIApp(t *testing.T) {
	got, err := RenderLambdaEntry(UnitTemplateData{Unit: "worker", EntryModule: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	src := string(got)
	assertParses(t, src)
	if strings.Contains(src, "Mangum") {
		t.Errorf("a non-HTTP unit should not pull in Mangum:\n%s", src)
	}
	if !strings.Contains(src, "_cc_pubsub.dispatch(event)") {
		t.Errorf("subscriber dispatch is missing:\n%s", src)
	}
}

func TestRenderDockerfile(t *testing.T) {
	got, err := RenderDockerfile(UnitTemplateData{Unit: "api", EntryModule: "app", ASGIApp: "app"})
	if err != nil {
		t.Fatal(err)
	}
	src := string(got)
	if !strings.Contains(src, "FROM python:"+PythonVersion) {
		t.Errorf("Dockerfile should target Python %s:\n%s", PythonVersion, src)
	}
	if !strings.Contains(src, "uvicorn app:app") {
		t.Errorf("missing the uvicorn command:\n%s", src)
	}
}

func TestRenderPackageScript(t *testing.T) {
	got, err := RenderPackageScript([]string{"worker", "api"})
	if err != nil {
		t.Fatal(err)
	}
	src := string(got)
	if !strings.Contains(src, "uv pip install") {
		t.Errorf("packaging must install dependencies; Pulumi will not:\n%s", src)
	}
	// Units appear in sorted order, so the script is byte-stable.
	if strings.Index(src, "unit api") > strings.Index(src, "unit worker") {
		t.Errorf("units are not sorted:\n%s", src)
	}
	if !strings.Contains(src, "--python-version \""+PythonVersion+"\"") &&
		!strings.Contains(src, "PY_VERSION=\""+PythonVersion+"\"") {
		t.Errorf("the packaging script should pin the interpreter version:\n%s", src)
	}
}

func TestMergeRequirements(t *testing.T) {
	existing := []byte("# my deps\nfastapi==0.115.6\nboto3==1.20.0\n")
	got := string(MergeRequirements(existing, []string{"boto3>=1.34", "mangum>=0.17"}))
	want := "# my deps\nboto3==1.20.0\nfastapi==0.115.6\nmangum>=0.17\n"
	if got != want {
		t.Errorf("MergeRequirements =\n%q\nwant\n%q", got, want)
	}
}

func TestMergeRequirementsFromEmpty(t *testing.T) {
	got := string(MergeRequirements(nil, []string{"mangum>=0.17", "boto3>=1.34"}))
	if got != "boto3>=1.34\nmangum>=0.17\n" {
		t.Errorf("MergeRequirements = %q", got)
	}
}

func TestMergeRequirementsKeepsPipOptions(t *testing.T) {
	got := string(MergeRequirements([]byte("--index-url https://example.test\nfastapi\n"), []string{"boto3>=1.34"}))
	if !strings.HasPrefix(got, "--index-url https://example.test\n") {
		t.Errorf("pip options should stay at the top: %q", got)
	}
}

func TestDistributionName(t *testing.T) {
	cases := map[string]string{
		"boto3>=1.34":              "boto3",
		"Fastapi==0.1":             "fastapi",
		"uvicorn[standard]":        "uvicorn",
		"pkg ; python_version>'3'": "pkg",
		"thing @ https://x/y.whl":  "thing",
	}
	for in, want := range cases {
		if got := distributionName(in); got != want {
			t.Errorf("distributionName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestShimRequirementsAreKnown(t *testing.T) {
	if !reflect.DeepEqual(ShimRequirements["base"], []string{"boto3>=1.34"}) {
		t.Errorf("base requirements = %v", ShimRequirements["base"])
	}
}
