package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/cloudcompiler/cloudcc/internal/lang"
	"github.com/spf13/cobra"
)

// MinistackEndpointEnv names the environment variable holding the AWS-compatible
// emulator endpoint. It is never hardcoded anywhere else.
const MinistackEndpointEnv = "CLOUDCC_EMULATOR_ENDPOINT"

// DefaultMinistackEndpoint is the conventional emulator address.
const DefaultMinistackEndpoint = "http://localhost:4566"

// MinistackEndpoint returns the configured emulator endpoint.
func MinistackEndpoint() string {
	if v := os.Getenv(MinistackEndpointEnv); v != "" {
		return v
	}
	return DefaultMinistackEndpoint
}

// tool is one entry in the doctor checklist.
type tool struct {
	name     string
	binary   string
	required bool
	brew     string
	why      string
	// present overrides the binary lookup for things that are not on PATH.
	// A Python package is the case that forced it: `diagrams` is what turns
	// architecture.py into a picture, and a checklist that only knows how to
	// look for executables reports every tool green while that picture is
	// silently never produced.
	present func() (string, bool)
}

var toolchain = []tool{
	{name: "go", binary: "go", required: false, brew: "brew install go", why: "only needed to build cloudcc from source"},
	{name: "pulumi", binary: "pulumi", required: true, brew: "brew install pulumi", why: "runs the generated infrastructure project"},
	{name: "node", binary: "node", required: false, brew: "brew install node", why: "type-checks the generated TypeScript"},
	{name: "dot", binary: "dot", required: false, brew: "brew install graphviz", why: "renders the topology diagram as PNG"},
	{
		name: "diagrams", required: false, brew: "pip install diagrams",
		why:     "renders architecture.py as an icon diagram; without it that file is still written, but no PNG",
		present: pythonPackage("diagrams"),
	},
	{name: "docker", binary: "docker", required: false, brew: "brew install --cask docker", why: "runs the AWS emulator and builds container images"},
	{name: "aws", binary: "aws", required: false, brew: "brew install awscli", why: "used by the integration tests to assert provisioned resources"},
}

// allTools is the fixed checklist plus whatever each registered language
// frontend needs, so adding a language does not mean editing this file.
func allTools() []tool {
	out := append([]tool(nil), toolchain...)
	for _, front := range lang.All() {
		for _, t := range front.Tools() {
			out = append(out, tool{
				name: t.Name, binary: t.Binary, required: t.Required,
				brew: t.Install, why: t.Why,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

func newDoctorCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check that the local toolchain is ready",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			w := cmd.OutOrStdout()
			missingRequired := 0

			for _, t := range allTools() {
				path, err := exec.LookPath(t.binary)
				if t.present != nil {
					var ok bool
					path, ok = t.present()
					err = nil
					if !ok {
						err = errNotPresent
					}
				}
				switch {
				case err == nil:
					fmt.Fprintf(w, "  ok       %-8s %s\n", t.name, path)
				case t.required:
					missingRequired++
					fmt.Fprintf(w, "  MISSING  %-8s required -- %s\n", t.name, t.why)
					fmt.Fprintf(w, "           install with: %s\n", t.brew)
				default:
					fmt.Fprintf(w, "  absent   %-8s optional -- %s\n", t.name, t.why)
					fmt.Fprintf(w, "           install with: %s\n", t.brew)
				}
			}

			endpoint := MinistackEndpoint()
			if reachable(endpoint) {
				fmt.Fprintf(w, "  ok       %-8s %s\n", "emulator", endpoint)
			} else {
				fmt.Fprintf(w, "  absent   %-8s %s is not answering -- integration tests will skip\n",
					"emulator", endpoint)
				fmt.Fprintf(w, "           start one and set %s if it listens elsewhere\n", MinistackEndpointEnv)
			}

			if credentialsPresent() {
				fmt.Fprintf(w, "  ok       %-8s found in the environment or ~/.aws\n", "aws-creds")
			} else {
				fmt.Fprintf(w, "  absent   %-8s no credentials found; only emulator deploys will work\n", "aws-creds")
			}

			if missingRequired > 0 {
				return exitError{ExitCompile, fmt.Errorf("%d required tool(s) missing", missingRequired)}
			}
			fmt.Fprintln(w, "\nAll required tools are present.")
			return nil
		},
	}
}

// errNotPresent stands in for exec.LookPath's error when a tool is checked
// some other way.
var errNotPresent = errors.New("not present")

// pythonPackage reports whether an importable package is installed, using the
// same interpreter and the same probe the renderer itself uses -- so the
// checklist cannot say yes to something the compile will then say no to.
func pythonPackage(name string) func() (string, bool) {
	return func() (string, bool) {
		python, err := exec.LookPath("python3")
		if err != nil {
			return "", false
		}
		if err := exec.Command(python, "-c", "import "+name).Run(); err != nil {
			return "", false
		}
		return python + " (import " + name + ")", true
	}
}

// reachable reports whether something is listening at the endpoint. Any HTTP
// response counts: the emulator answers unauthenticated probes with an error
// status, which still proves it is up.
func reachable(endpoint string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		// Fall back to a bare TCP dial, in case the endpoint speaks something
		// other than HTTP on a plain host:port.
		host := strings.TrimPrefix(strings.TrimPrefix(endpoint, "http://"), "https://")
		conn, derr := net.DialTimeout("tcp", host, time.Second)
		if derr != nil {
			return false
		}
		conn.Close()
		return true
	}
	resp.Body.Close()
	return true
}

func credentialsPresent() bool {
	if os.Getenv("AWS_ACCESS_KEY_ID") != "" || os.Getenv("AWS_PROFILE") != "" {
		return true
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	for _, name := range []string{"credentials", "config"} {
		if _, err := os.Stat(home + "/.aws/" + name); err == nil {
			return true
		}
	}
	return false
}
