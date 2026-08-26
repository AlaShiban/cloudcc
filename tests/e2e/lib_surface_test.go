// Package e2e_test holds checks about the shell harness that are cheap enough
// to run offline, so a broken harness fails in `make check` rather than eight
// minutes into a deploy.
package e2e_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Every helper the harnesses call must still be defined.
//
// `bash -n` does not catch a missing function -- it is a runtime lookup -- so a
// helper deleted from lib.sh fails only when that line is reached, which for
// the ones below is after compiling, packaging and a full `pulumi up`. It
// happened twice in one sitting: a block edit to lib.sh took `seed_secrets`,
// `py_run_deps` and two others with it, and each was found by a separate
// eight-minute run.
//
// The list is explicit rather than inferred. Working out which words in a shell
// script are function calls means writing a shell parser, and a heuristic that
// is wrong in either direction is worse than a list that has to be added to --
// adding a helper here is one line, and forgetting to costs nothing.
var harnessHelpers = []string{
	"aws_local", "app_name", "app_out",
	"ensure_engine", "ensure_engines", "reset_engines", "stop_engines",
	"ensure_local_bucket", "reset_local_table", "local_aws_env",
	"emulator_python_target", "engine_bindings_local", "export_engine_bindings_local",
	"probe_service", "pulumi_configure_emulator", "py_run_deps",
	"require_endpoint", "seed_secrets", "skip_unless_service",
	"unit_field", "unit_language", "unit_target", "wait_for_http",
}

func TestEveryHarnessHelperIsDefined(t *testing.T) {
	lib, err := os.ReadFile("lib.sh")
	if err != nil {
		t.Fatalf("reading lib.sh: %v", err)
	}
	defined := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^([a-z_][a-z0-9_]*)\(\)`).FindAllStringSubmatch(string(lib), -1) {
		defined[m[1]] = true
	}

	var missing []string
	for _, name := range harnessHelpers {
		if !defined[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("tests/e2e/lib.sh no longer defines: %s\n"+
			"A harness calling one of these fails only when that line runs, which is "+
			"after a full deploy.", strings.Join(missing, ", "))
	}
}

// The reverse: a helper defined and used nowhere is dead weight, and dead
// weight in a harness is read as "this is how it is done" by whoever adds the
// next one.
func TestEveryDefinedHelperIsUsed(t *testing.T) {
	lib, err := os.ReadFile("lib.sh")
	if err != nil {
		t.Fatalf("reading lib.sh: %v", err)
	}
	scripts, err := filepath.Glob("*.sh")
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	for _, path := range scripts {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		all.Write(data)
	}
	body := all.String()

	for _, m := range regexp.MustCompile(`(?m)^([a-z_][a-z0-9_]*)\(\)`).FindAllStringSubmatch(string(lib), -1) {
		name := m[1]
		// One definition plus at least one call. `stop_engines` is deliberately
		// exempt: it is the manual escape hatch for the containers, which are
		// reused across runs rather than torn down by any harness.
		if name == "stop_engines" {
			continue
		}
		if strings.Count(body, name) < 2 {
			t.Errorf("lib.sh defines %s() and nothing calls it", name)
		}
	}
}
