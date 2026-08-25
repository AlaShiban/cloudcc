package topology

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The generated program is only useful if it runs, and the two ways it can
// fail to are both silent from Go's side: a class name that does not exist in
// the diagrams package, and a call site that names a class the imports did not
// bind. Both produce no picture and no compile error.
//
// The second one happened while this was being written -- every call site was
// emitted fully qualified (aws.compute.Lambda) against `from diagrams.aws
// .compute import Lambda`, which is a NameError on the first node.

func TestCallSitesUseTheImportedName(t *testing.T) {
	p := archProgram(t)
	got := string(Python(p, Options{App: "demo", View: Resources}))

	if !strings.Contains(got, "from diagrams.aws.compute import Lambda") {
		t.Errorf("expected a compute import:\n%s", got)
	}
	// The name that was imported, not the path it came from.
	if !strings.Contains(got, "= Lambda(") {
		t.Errorf("a call site should use the imported name:\n%s", got)
	}
	if strings.Contains(got, "= aws.compute.Lambda(") {
		t.Errorf("a fully qualified call site is a NameError at render time:\n%s", got)
	}
}

func TestEveryDrawnNodeIsImported(t *testing.T) {
	p := archProgram(t)
	got := string(Python(p, Options{App: "demo", View: Resources}))

	bound := map[string]bool{}
	for _, line := range strings.Split(got, "\n") {
		_, names, ok := strings.Cut(line, " import ")
		if !ok || !strings.HasPrefix(line, "from diagrams") {
			continue
		}
		for _, name := range strings.Split(names, ", ") {
			bound[strings.TrimSpace(name)] = true
		}
	}

	for _, line := range strings.Split(got, "\n") {
		trimmed := strings.TrimSpace(line)
		_, call, ok := strings.Cut(trimmed, " = ")
		if !ok {
			continue
		}
		name, _, _ := strings.Cut(call, "(")
		if !bound[name] {
			t.Errorf("%q is constructed but never imported", name)
		}
	}
}

// This file draws the architecture, not the resource graph: one icon per
// capability, and none for the supporting cast. Nobody draws the execution role
// when they sketch a service on a whiteboard, and a picture in which a
// three-service application arrives as ten icons is not one anybody reads
// twice. The exhaustive view lives in architecture.mmd.
func TestOnlyThePrincipalServiceOfEachCapabilityIsDrawn(t *testing.T) {
	p := archProgram(t)
	got := string(Python(p, Options{App: "demo", View: Resources}))

	if !strings.Contains(got, `Lambda("main`) {
		t.Errorf("the unit's compute should be drawn:\n%s", got)
	}
	if !strings.Contains(got, `Dynamodb("pets`) {
		t.Errorf("the store should be drawn:\n%s", got)
	}
	for _, supporting := range []string{"IAMRole", "IAMPermissions", "CloudwatchLogs"} {
		if strings.Contains(got, supporting) {
			t.Errorf("%s is supporting cast and belongs in architecture.mmd, "+
				"not in the architecture picture:\n%s", supporting, got)
		}
	}
}

// The label says what the program called it and which service it became --
// "petsByOwner / DynamoDB", not "petsByOwner / aws.dynamodb".
func TestLabelsNameTheServiceNotThePulumiType(t *testing.T) {
	p := archProgram(t)
	got := string(Python(p, Options{App: "demo", View: Resources}))

	if !strings.Contains(got, `\nDynamoDB"`) {
		t.Errorf("expected a service label:\n%s", got)
	}
	if strings.Contains(got, "aws.dynamodb\"") {
		t.Errorf("a Pulumi type leaked into a label:\n%s", got)
	}
}

// The whole file must be valid Python. Checked by compiling rather than
// importing, so it needs no packages installed.
func TestTheGeneratedProgramParses(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	p := archProgram(t)
	src := filepath.Join(t.TempDir(), PythonFile)
	if err := os.WriteFile(src, Python(p, Options{App: "demo", View: Resources}), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command(python, "-c",
		"import sys; compile(open(sys.argv[1]).read(), sys.argv[1], 'exec')", src).CombinedOutput()
	if err != nil {
		t.Errorf("the generated program does not parse:\n%s", out)
	}
}

// Every class in the mapping must exist in the diagrams package. A wrong name
// is an ImportError at render time and the picture simply never appears --
// which looks exactly like "the package is not installed".
//
// Skipped where diagrams is absent rather than passing quietly; CI installs it.
func TestEveryMappedClassExists(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	if err := exec.Command(python, "-c", "import diagrams").Run(); err != nil {
		t.Skip("the diagrams package is not installed (pip install diagrams)")
	}

	classes := make([]string, 0, len(diagramsNode)+1)
	for _, cls := range diagramsNode {
		classes = append(classes, cls)
	}
	classes = append(classes, fallbackNode)
	sort.Strings(classes)

	const script = `
import importlib, sys
missing = []
for spec in sys.argv[1:]:
    module, _, name = spec.rpartition(".")
    try:
        mod = importlib.import_module("diagrams." + module)
    except ImportError:
        missing.append(spec + " (no such module)")
        continue
    if not hasattr(mod, name):
        missing.append(spec)
if missing:
    print("\n".join(sorted(set(missing))))
    sys.exit(1)
`
	out, err := exec.Command(python, append([]string{"-c", script}, classes...)...).CombinedOutput()
	if err != nil {
		t.Errorf("these classes do not exist in the diagrams package:\n%s", out)
	}
}
