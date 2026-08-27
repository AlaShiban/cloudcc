package aws

import (
	"strings"
	"testing"

	"github.com/cloudcompiler/cloudcc/internal/config"
	runtimepy "github.com/cloudcompiler/cloudcc/internal/runtime/py"
)

func args(t *testing.T, resources map[string]any) (map[string]any, error) {
	t.Helper()
	return LambdaResourceArgs("api", config.ResourceConfig{Type: "lambda", Resources: resources})
}

// The portable spelling goes in, and Pulumi's spelling comes out.
//
// This is the whole contract. cloudcc.yaml is written the way OpenTofu and
// Terraform are written -- and the way Pulumi's own Python and YAML SDKs are
// written -- so the file does not have to be revisited when the backend
// changes. Only the emitter knows about camelCase.
func TestArgumentsAreTranslatedToTheBackendsSpelling(t *testing.T) {
	got, err := args(t, map[string]any{
		"memory_size":                    1024,
		"timeout":                        60,
		"architectures":                  []any{"arm64"},
		"reserved_concurrent_executions": 5,
		"ephemeral_storage":              map[string]any{"size": 2048},
		"snap_start":                     map[string]any{"apply_on": "PublishedVersions"},
		"tracing_config":                 map[string]any{"mode": "Active"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for key, want := range map[string]any{
		"memorySize":                   1024,
		"timeout":                      60,
		"reservedConcurrentExecutions": 5,
	} {
		if got[key] != want {
			t.Errorf("%s = %v (%T), want %v", key, got[key], got[key], want)
		}
	}
	for _, snake := range []string{"memory_size", "reserved_concurrent_executions", "ephemeral_storage"} {
		if _, present := got[snake]; present {
			t.Errorf("%q reached the backend unchanged; it should have been translated", snake)
		}
	}

	// Nested blocks translate at every level, which is the part that would
	// quietly half-work: `ephemeralStorage: {size: N}` is valid TypeScript for
	// an object with the wrong shape only if the inner key is right too.
	storage, ok := got["ephemeralStorage"].(map[string]any)
	if !ok {
		t.Fatalf("ephemeralStorage = %#v, want a block", got["ephemeralStorage"])
	}
	if storage["size"] != 2048 {
		t.Errorf("ephemeralStorage.size = %v, want 2048", storage["size"])
	}
	snap, ok := got["snapStart"].(map[string]any)
	if !ok || snap["applyOn"] != "PublishedVersions" {
		t.Errorf("snapStart = %#v, want {applyOn: PublishedVersions}", got["snapStart"])
	}
	trace, ok := got["tracingConfig"].(map[string]any)
	if !ok || trace["mode"] != "Active" {
		t.Errorf("tracingConfig = %#v, want {mode: Active}", got["tracingConfig"])
	}
}

// Every argument in the table must translate, and no two may collide.
//
// A missing Pulumi name would emit a property called "" and a duplicate would
// silently drop one argument, and both are the kind of thing a table invites.
func TestEveryArgumentHasADistinctBackendName(t *testing.T) {
	var walk func(prefix string, schema []lambdaArg)
	seen := map[string]string{}
	walk = func(prefix string, schema []lambdaArg) {
		for _, a := range schema {
			if a.Name == "" || a.Pulumi == "" {
				t.Errorf("%s%q: both names are required, got %q/%q", prefix, a.Name, a.Name, a.Pulumi)
			}
			if a.Why == "" {
				t.Errorf("%s%q has no Why, so the diagnostic listing it would say nothing", prefix, a.Name)
			}
			key := prefix + a.Pulumi
			if first, dup := seen[key]; dup {
				t.Errorf("%s%q and %q both emit %q", prefix, first, a.Name, a.Pulumi)
			}
			seen[key] = a.Name
			if a.Kind == argBlock {
				if len(a.Fields) == 0 {
					t.Errorf("%s%q is a block with no fields", prefix, a.Name)
				}
				walk(a.Name+".", a.Fields)
			}
		}
	}
	walk("", lambdaArgs)
}

// A value AWS would refuse is refused here, where the message can name the
// file it was written in.
func TestValuesOutsideWhatAWSAcceptsAreRejected(t *testing.T) {
	for _, tc := range []struct {
		name      string
		resources map[string]any
		wants     string
	}{
		{"memory below the floor", map[string]any{"memory_size": 64}, "between 128 and 10240"},
		{"memory above the ceiling", map[string]any{"memory_size": 20000}, "between 128 and 10240"},
		{"timeout above the ceiling", map[string]any{"timeout": 901}, "between 1 and 900"},
		{"timeout of zero", map[string]any{"timeout": 0}, "between 1 and 900"},
		{"tmp below the floor", map[string]any{"ephemeral_storage": map[string]any{"size": 128}}, "between 512 and 10240"},
		{"two architectures", map[string]any{"architectures": []any{"arm64", "x86_64"}}, "exactly one architecture"},
		{"an architecture that does not exist", map[string]any{"architectures": []any{"riscv"}}, `"arm64" or "x86_64"`},
		{"a tracing mode that does not exist", map[string]any{"tracing_config": map[string]any{"mode": "Sometimes"}}, `"Active" or "PassThrough"`},
		{"negative concurrency", map[string]any{"reserved_concurrent_executions": -5}, "must be -1"},
		{"memory as text", map[string]any{"memory_size": "1024"}, "must be a whole number"},
		{"a block given a scalar", map[string]any{"ephemeral_storage": 2048}, "takes a block of settings"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := args(t, tc.resources)
			if err == nil {
				t.Fatalf("accepted %v", tc.resources)
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("error does not explain the limit.\n  got:  %v\n  want it to contain: %q", err, tc.wants)
			}
		})
	}
}

// The commonest mistake is copying an argument out of a Pulumi program, and the
// diagnostic has to be better than "unknown key" for that to be a small
// mistake rather than a confusing one.
func TestACamelCaseSpellingIsNamedAsTheMistakeItIs(t *testing.T) {
	_, err := args(t, map[string]any{"memorySize": 1024})
	if err == nil {
		t.Fatal("memorySize was accepted; only the portable spelling should be")
	}
	msg := err.Error()
	for _, want := range []string{`"memorySize"`, `Did you mean "memory_size"?`, "OpenTofu"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the diagnostic is missing %q:\n%s", want, msg)
		}
	}
}

// An unknown argument lists what is supported, rather than leaving the reader
// to find the table.
func TestAnUnknownArgumentListsWhatIsSupported(t *testing.T) {
	_, err := args(t, map[string]any{"cpu": 256})
	if err == nil {
		t.Fatal("cpu was accepted on a Lambda")
	}
	for _, want := range []string{"memory_size", "timeout", "ephemeral_storage"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the diagnostic does not list %q:\n%s", want, err)
		}
	}
}

// What the compiler derives cannot be overridden, and the refusal says why.
//
// These are not merely unsupported. Setting `handler` would produce a function
// whose code and whose declared entrypoint disagree, and the failure arrives at
// the first invocation as an import error naming neither.
func TestArgumentsTheCompilerOwnsAreRefusedWithAReason(t *testing.T) {
	for _, tc := range []struct{ arg, wants string }{
		{"handler", "generated entrypoint"},
		{"runtime", "the language the unit is written in"},
		{"role", "least privilege"},
		{"function_name", "every binding that points at this function"},
		{"environment", "environment_variables"},
	} {
		t.Run(tc.arg, func(t *testing.T) {
			_, err := args(t, map[string]any{tc.arg: "anything"})
			if err == nil {
				t.Fatalf("%s was accepted", tc.arg)
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("the refusal does not explain itself.\n  got:  %v\n  want it to contain: %q", err, tc.wants)
			}
		})
	}
}

// Nothing configured means nothing added, so the defaults in compute.go stand
// and a project that sets none of this emits exactly what it emitted before.
func TestNoBlockMeansNoArguments(t *testing.T) {
	got, err := LambdaResourceArgs("api", config.ResourceConfig{Type: "lambda"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want nothing", got)
	}
}

// A block where nothing reads it is an error, not a no-op.
func TestResourcesSomewhereNothingReadsItIsRefused(t *testing.T) {
	sized := map[string]any{"memory_size": 1024}

	t.Run("on a store", func(t *testing.T) {
		app := &config.App{
			App:       "shop",
			Persisted: map[string]config.ResourceConfig{"petsByOwner": {Type: "dynamodb", Resources: sized}},
		}
		err := CheckResourcesAreSupported(app)
		if err == nil {
			t.Fatal("a resources block on a DynamoDB table was accepted")
		}
		if !strings.Contains(err.Error(), "only supported on execution units") {
			t.Errorf("unhelpful error: %v", err)
		}
	})

	t.Run("on a container unit", func(t *testing.T) {
		app := &config.App{
			App:            "shop",
			ExecutionUnits: map[string]config.ResourceConfig{"reporter": {Type: "ecs", Resources: sized}},
		}
		err := CheckResourcesAreSupported(app)
		if err == nil {
			t.Fatal("a resources block on an ECS unit was accepted")
		}
		if !strings.Contains(err.Error(), "cpu and memory units") {
			t.Errorf("the error does not say why the arguments differ: %v", err)
		}
	})

	t.Run("on a lambda unit", func(t *testing.T) {
		app := &config.App{
			App:            "shop",
			ExecutionUnits: map[string]config.ResourceConfig{"api": {Type: "lambda", Resources: sized}},
		}
		if err := CheckResourcesAreSupported(app); err != nil {
			t.Errorf("a resources block on a Lambda was refused: %v", err)
		}
	})
}

// The architecture a unit declares and the architecture its wheels are built
// for must be the same, and one reader is what keeps them so.
//
// A mismatch is not a warning. An architecture is part of a compiled
// extension's filename, so a bundle built for the wrong one installs cleanly,
// zips cleanly, deploys cleanly and fails on its first invocation with "No
// module named X" -- which names neither the architecture nor the file the
// mismatch was written in. That exact failure cost a CI round in this
// repository when a harness packaged manylinux wheels for a musl runtime.
func TestTheDeclaredArchitectureIsTheOneWheelsAreBuiltFor(t *testing.T) {
	for _, tc := range []struct{ declared, want string }{
		{"arm64", "aarch64-manylinux2014"},
		{"x86_64", "x86_64-manylinux2014"},
		{"", "x86_64-manylinux2014"},
	} {
		name := tc.declared
		if name == "" {
			name = "unset"
		}
		t.Run(name, func(t *testing.T) {
			cfg := config.ResourceConfig{Type: "lambda"}
			if tc.declared != "" {
				cfg.Resources = map[string]any{"architectures": []any{tc.declared}}
			}

			if got := cfg.Architecture(); got != tc.declared {
				t.Fatalf("Architecture() = %q, want %q", got, tc.declared)
			}
			if got := runtimepy.PlatformFor(cfg.Architecture()); got != tc.want {
				t.Errorf("wheels would be built for %q while the function declares %q",
					got, tc.declared)
			}

			// And the argument still reaches the backend, so the function
			// really is created with the architecture the wheels assume.
			if tc.declared == "" {
				return
			}
			emitted, err := LambdaResourceArgs("api", cfg)
			if err != nil {
				t.Fatal(err)
			}
			list, ok := emitted["architectures"].([]any)
			if !ok || len(list) != 1 || list[0] != tc.declared {
				t.Errorf("architectures reached the backend as %#v", emitted["architectures"])
			}
		})
	}
}
