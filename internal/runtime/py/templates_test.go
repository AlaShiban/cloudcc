package py

import (
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

// assertParses runs generated source through the real interpreter's parser.
// Shipping a template that is not valid Python is the failure that matters
// most here, and tree-sitter is too forgiving to catch it.
func assertParses(t *testing.T, src string) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not on PATH")
	}
	cmd := exec.Command(python, "-c", "import ast,sys; ast.parse(sys.stdin.read())")
	cmd.Stdin = strings.NewReader(src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated source is not valid Python: %v\n%s\n--- source ---\n%s", err, out, src)
	}
}

func TestRuntimeFilesAreEmbedded(t *testing.T) {
	files, err := RuntimeFiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"_cloudcc_runtime/__init__.py",
		"_cloudcc_runtime/_client.py",
		"_cloudcc_runtime/kv.py",
		"_cloudcc_runtime/fs.py",
		"_cloudcc_runtime/secret.py",
		"_cloudcc_runtime/orm.py",
		"_cloudcc_runtime/redis_.py",
		"_cloudcc_runtime/pubsub.py",
		"_cloudcc_runtime/config.py",
		"_cloudcc_runtime/expose.py",
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
	if !strings.Contains(string(files["_cloudcc_runtime/_client.py"]), "import boto3") {
		t.Error("_client.py should be where boto3 lives")
	}
	for name, content := range files {
		if name == "_cloudcc_runtime/_client.py" {
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
		"_cloudcc_pubsub.is_sns_event(event)",
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
	if !strings.Contains(src, "_cloudcc_pubsub.dispatch(event)") {
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

func TestPushScriptTargetsTheExportedRepository(t *testing.T) {
	got, err := RenderPushScript([]PackageUnit{{ID: "api"}, {ID: "reporter", Container: true}})
	if err != nil {
		t.Fatal(err)
	}
	src := string(got)
	if !strings.Contains(src, `push "reporter" "CLOUDCC_ECR_REPORTER_URL"`) {
		t.Errorf("the push script should read the repository from the stack output:\n%s", src)
	}
	if strings.Contains(src, `push "api"`) {
		t.Errorf("a zip-packaged unit has no image to push:\n%s", src)
	}
}

func TestShimRequirementsAreKnown(t *testing.T) {
	if !reflect.DeepEqual(ShimRequirements["base"], []string{"boto3>=1.34"}) {
		t.Errorf("base requirements = %v", ShimRequirements["base"])
	}
}

// A module can import the SDK for a type annotation, or keep an import it no
// longer uses. The SDK is not installed in a deployment bundle, so that import
// has to go even though there is nothing to rewrite.

// The packaging script is now language-neutral: each unit contributes the
// shell that builds it, so a Node unit and a Python unit can sit in one script
// without it knowing about either.
func TestPackageScriptStitchesFragments(t *testing.T) {
	got, err := RenderPackageScript([]PackageUnit{
		{ID: "worker", Fragment: "echo worker-fragment\n"},
		{ID: "api", Fragment: "echo api-fragment\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	src := string(got)
	for _, want := range []string{"echo api-fragment", "echo worker-fragment"} {
		if !strings.Contains(src, want) {
			t.Errorf("missing %q:\n%s", want, src)
		}
	}
	// Units are emitted in sorted order, so the script is byte-stable.
	if strings.Index(src, "api-fragment") > strings.Index(src, "worker-fragment") {
		t.Errorf("units are not sorted:\n%s", src)
	}
	// Nothing language-specific survives in the outer script.
	for _, leaked := range []string{"uv pip", "PY_VERSION", "requirements.txt"} {
		if strings.Contains(src, leaked) {
			t.Errorf("the outer script should not mention %q:\n%s", leaked, src)
		}
	}
}

func TestPackagingFragments(t *testing.T) {
	zip := PackagingScript("api", false)
	if !strings.Contains(zip, "uv pip install") {
		t.Errorf("a zip-packaged unit installs its dependencies:\n%s", zip)
	}
	if !strings.Contains(zip, `"../api.zip"`) {
		t.Errorf("a zip-packaged unit produces an archive:\n%s", zip)
	}
	// Both halves of the target are pinned, and both default to what the
	// deployed function actually is. A wheel with a compiled extension carries
	// the interpreter and the architecture in its filename, so getting either
	// wrong produces a bundle that installs, zips and deploys cleanly and then
	// cannot import itself.
	if !strings.Contains(zip, "${CLOUDCC_PYTHON_VERSION:-"+PythonVersion+"}") {
		t.Errorf("the fragment should default to the declared interpreter:\n%s", zip)
	}
	if !strings.Contains(zip, "${CLOUDCC_PYTHON_PLATFORM:-x86_64-manylinux2014}") {
		t.Errorf("the fragment should default to Lambda's own architecture rather "+
			"than to whatever machine is doing the packaging:\n%s", zip)
	}

	container := PackagingScript("reporter", true)
	if !strings.Contains(container, `docker build --quiet --tag "cloudcc-reporter:latest"`) {
		t.Errorf("a container unit is built as an image:\n%s", container)
	}
	if strings.Contains(container, ".zip") {
		t.Errorf("a container unit is not zipped:\n%s", container)
	}
}
