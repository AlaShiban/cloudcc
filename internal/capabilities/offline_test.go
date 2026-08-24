package capabilities

import (
	"os/exec"
	"strings"
	"testing"
)

// Compilation is offline (D12, and the whole point of not depending on a
// remote rendering service). Rather than trusting that, this checks the
// dependency graph: if no package on the compile path can import a networking
// library, a compile cannot make a request by accident.
//
// internal/deploy is the one place networking is allowed, and it is
// deliberately not on this path -- the compile path never imports it, which is
// also what keeps the Automation API's weight out of `cloudcc compile`.
func TestTheCompilePathCannotReachTheNetwork(t *testing.T) {
	// Only cloudcc's own packages are checked. A library on the path may well pull
	// in net/http transitively -- afero does, for its HTTP filesystem -- but
	// that is code the compiler never calls. What matters is that no stage of
	// the compile reaches for a network client itself.
	out, err := exec.Command("go", "list", "-deps",
		"github.com/cloudcompiler/cloudcc/internal/capabilities").Output()
	if err != nil {
		t.Fatalf("listing dependencies failed: %v", err)
	}

	var ours []string
	for _, dep := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.HasPrefix(dep, "github.com/cloudcompiler/cloudcc/") {
			ours = append(ours, dep)
		}
	}
	if len(ours) < 5 {
		t.Fatalf("expected the compile path to span several cloudcc packages, found %v", ours)
	}

	banned := map[string]string{
		"net/http": "an HTTP client",
		"net/rpc":  "an RPC client",
		"net/smtp": "a mail client",
		"github.com/pulumi/pulumi/sdk/v3/go/auto": "the Pulumi Automation API",
	}
	for _, pkg := range ours {
		imports, err := exec.Command("go", "list", "-f", "{{range .Imports}}{{.}}\n{{end}}", pkg).Output()
		if err != nil {
			t.Fatalf("listing imports of %s failed: %v", pkg, err)
		}
		for _, imp := range strings.Split(strings.TrimSpace(string(imports)), "\n") {
			if reason, bad := banned[imp]; bad {
				t.Errorf("%s imports %s (%s); compilation must not touch the network", pkg, imp, reason)
			}
		}
	}
}

// TestDeployIsIsolated pins the other half: the deploy package may reach the
// network, and nothing else may depend on it.
func TestDeployIsIsolated(t *testing.T) {
	for _, pkg := range []string{
		"github.com/cloudcompiler/cloudcc/internal/capabilities",
		"github.com/cloudcompiler/cloudcc/internal/compiler",
		"github.com/cloudcompiler/cloudcc/internal/provider/aws",
		"github.com/cloudcompiler/cloudcc/internal/iac/pulumi_ts",
	} {
		out, err := exec.Command("go", "list", "-deps", pkg).Output()
		if err != nil {
			t.Fatalf("listing dependencies of %s failed: %v", pkg, err)
		}
		if strings.Contains(string(out), "cloudcompiler/cloudcc/internal/deploy") {
			t.Errorf("%s depends on internal/deploy; deployment must stay isolated from compilation", pkg)
		}
	}
}
