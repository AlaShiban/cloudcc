package cli

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// MinistackEndpointEnv names the environment variable holding the AWS-compatible
// emulator endpoint. It is never hardcoded anywhere else.
const MinistackEndpointEnv = "MINISTACK_ENDPOINT"

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
}

var toolchain = []tool{
	{name: "go", binary: "go", required: false, brew: "brew install go", why: "only needed to build cc from source"},
	{name: "pulumi", binary: "pulumi", required: true, brew: "brew install pulumi", why: "runs the generated infrastructure project"},
	{name: "uv", binary: "uv", required: true, brew: "brew install uv", why: "installs Python dependencies when packaging execution units"},
	{name: "node", binary: "node", required: false, brew: "brew install node", why: "type-checks the generated TypeScript"},
	{name: "dot", binary: "dot", required: false, brew: "brew install graphviz", why: "renders the topology diagram as PNG"},
	{name: "docker", binary: "docker", required: false, brew: "brew install --cask docker", why: "runs the AWS emulator and builds container images"},
	{name: "aws", binary: "aws", required: false, brew: "brew install awscli", why: "used by the integration tests to assert provisioned resources"},
}

func newDoctorCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check that the local toolchain is ready",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			w := cmd.OutOrStdout()
			missingRequired := 0

			for _, t := range toolchain {
				path, err := exec.LookPath(t.binary)
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
