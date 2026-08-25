package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudcompiler/cloudcc/internal/ir"
)

// run executes the CLI as a user would and returns stdout, stderr and the exit
// code.
func run(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	root := NewRootCommand()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(args)

	code := execute(root)
	return stdout.String(), stderr.String(), code
}

// appOut is where a compile with `-o out` puts one application: out_dir holds
// a folder per app, so nothing is read from the root any more.
func appOut(out, app string) string { return filepath.Join(out, app) }

func writeApp(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range files {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestBareInvocationCompiles(t *testing.T) {
	src := writeApp(t, map[string]string{
		"app.py": "import cloudcompiler as cloudcc\npets = cloudcc.persist(boto3.resource(\"dynamodb\").Table(\"t\"), id=\"pets\")\n",
	})
	out := t.TempDir()

	// No `compile` subcommand: `cloudcc <path>` is the default action.
	_, stderr, code := run(t, src, "-o", out, "--app", "demo")
	if code != ExitOK {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(appOut(out, "demo"), "index.ts")); err != nil {
		t.Errorf("nothing was generated: %v", err)
	}
}

func TestExitCodes(t *testing.T) {
	good := writeApp(t, map[string]string{"app.py": "x = 1\n"})
	bad := writeApp(t, map[string]string{
		"app.py": "import cloudcompiler as cloudcc\nname = \"x\"\npets = cloudcc.persist(boto3.resource(\"dynamodb\").Table(\"t\"), id=name)\n",
	})

	cases := []struct {
		name string
		args []string
		want int
	}{
		{"success", []string{good, "-o", t.TempDir(), "--app", "demo"}, ExitOK},
		{"compile error", []string{bad, "-o", t.TempDir(), "--app", "demo"}, ExitCompile},
		{"unknown provider", []string{good, "-o", t.TempDir(), "--app", "demo", "--provider", "gcp"}, ExitUsage},
		{"missing directory", []string{filepath.Join(good, "nope"), "--app", "demo"}, ExitUsage},
		{"bad app name", []string{good, "-o", t.TempDir(), "--app", "has space"}, ExitUsage},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, stderr, code := run(t, c.args...)
			if code != c.want {
				t.Errorf("exit %d, want %d; stderr:\n%s", code, c.want, stderr)
			}
		})
	}
}

// Choosing a log destination that is planned but unimplemented must stop the
// compile, not fall back to CloudWatch. Nothing in the program mentions
// logging, so this is the one capability whose configuration could be dropped
// without anything noticing until the logs failed to arrive.
func TestAnUnimplementedLogDestinationIsRefused(t *testing.T) {
	src := writeApp(t, map[string]string{
		"app.py":       "import cloudcompiler as cloudcc\npets = cloudcc.persist(cloudcc.Topic(), id=\"pets\")\n",
		"cloudcc.yaml": "app: demo\nprovider: aws\nlogging:\n  type: datadog\n",
	})
	_, stderr, code := run(t, src, "-o", t.TempDir())
	if code != ExitCompile {
		t.Fatalf("exit %d, want %d; stderr:\n%s", code, ExitCompile, stderr)
	}
	if !strings.Contains(stderr, "datadog") || !strings.Contains(stderr, "cloudwatch") {
		t.Errorf("the error should name the choice and what is implemented:\n%s", stderr)
	}
}

func TestAnUnknownLogDestinationIsRefused(t *testing.T) {
	src := writeApp(t, map[string]string{
		"app.py":       "import cloudcompiler as cloudcc\npets = cloudcc.persist(cloudcc.Topic(), id=\"pets\")\n",
		"cloudcc.yaml": "app: demo\nprovider: aws\nlogging:\n  type: nonsense\n",
	})
	_, stderr, code := run(t, src, "-o", t.TempDir())
	if code != ExitCompile {
		t.Fatalf("exit %d, want %d; stderr:\n%s", code, ExitCompile, stderr)
	}
	if !strings.Contains(stderr, "unknown logging.type") {
		t.Errorf("stderr:\n%s", stderr)
	}
}

func TestNonLiteralArgumentIsReportedWithAPosition(t *testing.T) {
	src := writeApp(t, map[string]string{
		"app.py": "import cloudcompiler as cloudcc\nname = \"x\"\npets = cloudcc.persist(boto3.resource(\"dynamodb\").Table(\"t\"), id=name)\n",
	})
	_, stderr, code := run(t, src, "-o", t.TempDir(), "--app", "demo")
	if code != ExitCompile {
		t.Fatalf("exit %d, want %d", code, ExitCompile)
	}
	if !strings.Contains(stderr, "app.py:3:66: error: id must be a string literal") {
		t.Errorf("the error should point at the argument:\n%s", stderr)
	}
	if !strings.Contains(stderr, "no output written") {
		t.Errorf("the user should be told nothing was written:\n%s", stderr)
	}
}

func TestNoOutputIsWrittenWhenCompilationFails(t *testing.T) {
	src := writeApp(t, map[string]string{
		"app.py": "import cloudcompiler as cloudcc\nname = \"x\"\npets = cloudcc.persist(boto3.resource(\"dynamodb\").Table(\"t\"), id=name)\n",
	})
	out := t.TempDir()
	run(t, src, "-o", out, "--app", "demo")

	if _, err := os.Stat(filepath.Join(out, "index.ts")); err == nil {
		t.Error("a failed compile should not leave a generated project behind")
	}
}

func TestStrictTurnsWarningsIntoErrors(t *testing.T) {
	files := map[string]string{
		"app.py":    "import cloudcompiler as cloudcc\npets = cloudcc.persist(boto3.resource(\"dynamodb\").Table(\"t\"), id=\"pets\")\n",
		"orphan.py": "x = 1\n", // reached by nothing
	}
	src := writeApp(t, files)

	_, stderr, code := run(t, src, "-o", t.TempDir(), "--app", "demo")
	if code != ExitOK {
		t.Fatalf("a warning alone should not fail a compile: exit %d\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "warning:") {
		t.Errorf("expected a warning about the unreachable file:\n%s", stderr)
	}

	src2 := writeApp(t, files)
	_, stderr2, code2 := run(t, src2, "-o", t.TempDir(), "--app", "demo", "--strict")
	if code2 != ExitCompile {
		t.Fatalf("--strict should turn that warning into a failure: exit %d\n%s", code2, stderr2)
	}
	if !strings.Contains(stderr2, "error:") {
		t.Errorf("expected the warning promoted to an error:\n%s", stderr2)
	}
}

// TestDumpIRIsStructured pins the --dump-ir contract: tests assert against the
// structure, not against rendered text, which is what the version field is for.
func TestDumpIRIsStructured(t *testing.T) {
	src := writeApp(t, map[string]string{
		"app.py": "from fastapi import FastAPI\nimport cloudcompiler as cloudcc\n" +
			"app = FastAPI()\npets = cloudcc.persist(boto3.resource(\"dynamodb\").Table(\"t\"), id=\"pets\")\ncloudcc.expose(app, id=\"api\")\n",
	})
	stdout, stderr, code := run(t, src, "-o", t.TempDir(), "--app", "demo", "--dump-ir")
	if code != ExitOK {
		t.Fatalf("exit %d\n%s", code, stderr)
	}

	var dump ir.Dump
	if err := json.Unmarshal([]byte(stdout), &dump); err != nil {
		t.Fatalf("--dump-ir is not valid JSON: %v\n%s", err, stdout)
	}
	if dump.Version != ir.DumpVersion {
		t.Errorf("version = %d, want %d", dump.Version, ir.DumpVersion)
	}

	kinds := map[string]bool{}
	for _, in := range dump.Intents {
		kinds[in.Key.Kind] = true
	}
	for _, want := range []string{"execution_unit", "persist_kv", "expose"} {
		if !kinds[want] {
			t.Errorf("no %s intent in the dump: %v", want, kinds)
		}
	}
	if len(dump.Resources) == 0 {
		t.Error("the dump should include the resolved layer too")
	}

	var resolvesTo int
	for _, e := range dump.Edges {
		if e.Kind == ir.EdgeResolvesTo {
			resolvesTo++
		}
	}
	if resolvesTo == 0 {
		t.Error("resolves_to edges are what make the expansion inspectable; none were dumped")
	}
}

func TestInitScaffoldsAConfig(t *testing.T) {
	dir := t.TempDir()
	_, stderr, code := run(t, "init", dir, "--app", "demo")
	if code != ExitOK {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	data, err := os.ReadFile(filepath.Join(dir, "cloudcc.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "app: demo") {
		t.Errorf("the scaffolded config should name the app:\n%s", data)
	}

	// A second run must not silently overwrite it.
	_, _, code = run(t, "init", dir, "--app", "demo")
	if code != ExitUsage {
		t.Errorf("init should refuse to overwrite an existing cloudcc.yaml, exit %d", code)
	}
	_, _, code = run(t, "init", dir, "--app", "demo", "--force")
	if code != ExitOK {
		t.Errorf("--force should allow overwriting, exit %d", code)
	}
}

func TestInitOutputCompiles(t *testing.T) {
	src := writeApp(t, map[string]string{
		"app.py": "import cloudcompiler as cloudcc\npets = cloudcc.persist(boto3.resource(\"dynamodb\").Table(\"t\"), id=\"pets\")\n",
	})
	if _, _, code := run(t, "init", src, "--app", "demo"); code != ExitOK {
		t.Fatal("init failed")
	}
	if _, stderr, code := run(t, src, "-o", t.TempDir()); code != ExitOK {
		t.Fatalf("a scaffolded config should compile as-is: exit %d\n%s", code, stderr)
	}
}

func TestVersion(t *testing.T) {
	stdout, _, code := run(t, "version")
	if code != ExitOK || !strings.HasPrefix(stdout, "cloudcc ") {
		t.Errorf("version = %q, exit %d", stdout, code)
	}
}

func TestDiagramPrintsToStdout(t *testing.T) {
	src := writeApp(t, map[string]string{
		"app.py": "import cloudcompiler as cloudcc\npets = cloudcc.persist(boto3.resource(\"dynamodb\").Table(\"t\"), id=\"pets\")\n",
	})
	stdout, stderr, code := run(t, "diagram", src, "-o", t.TempDir(), "--app", "demo", "--format", "mermaid")
	if code != ExitOK {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "flowchart LR") {
		t.Errorf("expected a Mermaid diagram:\n%s", stdout)
	}

	_, _, code = run(t, "diagram", src, "-o", t.TempDir(), "--app", "demo", "--format", "png")
	if code != ExitUsage {
		t.Errorf("an unknown --format should be a usage error, exit %d", code)
	}
}

func TestStateFileRecordsTheFingerprint(t *testing.T) {
	src := writeApp(t, map[string]string{
		"app.py": "import cloudcompiler as cloudcc\npets = cloudcc.persist(boto3.resource(\"dynamodb\").Table(\"t\"), id=\"pets\")\n",
	})
	out := t.TempDir()
	if _, stderr, code := run(t, src, "-o", out, "--app", "demo"); code != ExitOK {
		t.Fatalf("exit %d\n%s", code, stderr)
	}

	data, err := os.ReadFile(filepath.Join(appOut(out, "demo"), StateFile))
	if err != nil {
		t.Fatal(err)
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatal(err)
	}
	if st.App != "demo" || len(st.Fingerprint) != 64 {
		t.Errorf("state = %+v", st)
	}
	if !strings.Contains(strings.Join(st.Units, ","), "main") {
		t.Errorf("units = %v", st.Units)
	}
}

func TestChangingSourceChangesTheFingerprint(t *testing.T) {
	src := writeApp(t, map[string]string{
		"app.py": "import cloudcompiler as cloudcc\npets = cloudcc.persist(boto3.resource(\"dynamodb\").Table(\"t\"), id=\"pets\")\n",
	})
	out := t.TempDir()
	run(t, src, "-o", out, "--app", "demo")
	before := readState(t, appOut(out, "demo")).Fingerprint

	if err := os.WriteFile(filepath.Join(src, "app.py"),
		[]byte("import cloudcompiler as cloudcc\npets = cloudcc.persist(boto3.resource(\"dynamodb\").Table(\"t\"), id=\"other\")\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, src, "-o", out, "--app", "demo")
	after := readState(t, appOut(out, "demo")).Fingerprint

	if before == after {
		t.Error("changing the source must change the fingerprint, or stale output cannot be detected")
	}
}

func readState(t *testing.T, dir string) State {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, StateFile))
	if err != nil {
		t.Fatal(err)
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatal(err)
	}
	return st
}
