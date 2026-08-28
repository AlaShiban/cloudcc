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
	"aws_local", "app_name", "app_out", "app_endpoint",
	"ensure_engine", "ensure_engines", "reset_engines", "stop_engines",
	"ensure_local_bucket", "reset_local_table", "local_aws_env",
	"emulator_python_target", "engine_bindings_local", "export_engine_bindings_local",
	"probe_service", "pulumi_configure_emulator", "py_run_deps",
	"require_endpoint", "scenario_databases", "seed_secrets", "skip_unless_service",
	"warm_emulator_rds",
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

// A harness that packages a unit must build for the runtime the emulator uses.
//
// The emulator runs Lambda containers of whatever image it has, with whatever
// libc, architecture and Python that brings -- aarch64 musl and Python 3.13 on
// one machine, something else on CI -- regardless of the runtime the function
// declares. bin/package.sh defaults to x86_64-manylinux2014, which is right for
// a real deployment and wrong here, so a harness has to ask
// emulator_python_target() and override it.
//
// provisioning.sh did not, and nothing noticed for a long time because no unit it
// deployed carried a compiled extension. The first one that did failed with
// "No module named 'psycopg2._psycopg'" -- which reads as a missing dependency
// rather than a wheel built for the wrong platform, and cost a CI round to
// diagnose.
func TestEveryPackagingHarnessBuildsForTheEmulatorsRuntime(t *testing.T) {
	scripts, err := filepath.Glob("*.sh")
	if err != nil {
		t.Fatal(err)
	}
	packages := regexp.MustCompile(`(?m)^[^#\n]*\./bin/package\.sh`)

	// Harnesses that package no Lambda bundle. The probe is about uv resolving
	// wheels for a zip that a Lambda runtime will unpack; a container unit's
	// dependencies are installed by pip *inside* the image, for the image's own
	// platform, so there is nothing here for it to get wrong.
	//
	// Listed rather than inferred, because "does this script deploy a Lambda"
	// is not something a grep can answer, and a heuristic that guessed wrong
	// would either nag forever or stop checking the harnesses that need it.
	containerOnly := map[string]string{
		"kubernetes.sh": "deploys one container unit; its wheels are installed by pip in the image",
	}

	for _, path := range scripts {
		if filepath.Base(path) == "lib.sh" {
			continue
		}
		if _, exempt := containerOnly[filepath.Base(path)]; exempt {
			continue
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		text := string(body)
		if !packages.MatchString(text) {
			continue
		}
		if !strings.Contains(text, "emulator_python_target") {
			t.Errorf("%s runs bin/package.sh but never calls emulator_python_target, "+
				"so it builds wheels for x86_64-manylinux2014 while the emulator's "+
				"Lambda runtime is something else. A unit with a compiled extension "+
				"will fail at import with a message that names the wrong cause.", path)
		}
	}
}
