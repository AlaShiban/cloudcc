package deploy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// compiled builds a directory that looks like cc's output, with the given
// fingerprint recorded.
func compiled(t *testing.T, fingerprint string) string {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "index.ts"), "// generated")
	if fingerprint != "" {
		state, err := json.Marshal(State{Version: 1, App: "petstore", Fingerprint: fingerprint})
		if err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(dir, StateFile), string(state))
	}
	return dir
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPreflightAcceptsMatchingOutput(t *testing.T) {
	_, err := Preflight(PreflightInput{
		Dir:                compiled(t, "abc123"),
		CurrentFingerprint: "abc123",
	})
	if err != nil {
		t.Fatalf("matching output should be accepted: %v", err)
	}
}

// TestPreflightRefusesStaleOutput is the point of the whole check (D19):
// deploying output that no longer matches the source is how a stack quietly
// diverges from the program that produced it.
func TestPreflightRefusesStaleOutput(t *testing.T) {
	_, err := Preflight(PreflightInput{
		Dir:                compiled(t, "old-fingerprint"),
		CurrentFingerprint: "new-fingerprint",
	})
	if err == nil {
		t.Fatal("stale output should be refused")
	}
	msg := err.Error()
	for _, want := range []string{"stale", "recompile", "--force"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal should mention %q: %s", want, msg)
		}
	}
}

func TestForceOverridesStaleness(t *testing.T) {
	warnings, err := Preflight(PreflightInput{
		Dir:                compiled(t, "old"),
		CurrentFingerprint: "new",
		Force:              true,
	})
	if err != nil {
		t.Fatalf("--force should allow a stale deploy: %v", err)
	}
	if len(warnings) == 0 {
		t.Error("--force should still warn that the output is stale")
	}
}

func TestPreflightRefusesAMissingDirectory(t *testing.T) {
	_, err := Preflight(PreflightInput{Dir: filepath.Join(t.TempDir(), "absent")})
	if err == nil || !strings.Contains(err.Error(), "cc compile") {
		t.Fatalf("the refusal should tell the user to compile: %v", err)
	}
}

func TestPreflightRefusesADirectoryThatIsNotCompiledOutput(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "notes.txt"), "unrelated")
	_, err := Preflight(PreflightInput{Dir: dir})
	if err == nil || !strings.Contains(err.Error(), "index.ts") {
		t.Fatalf("the refusal should say what is missing: %v", err)
	}
}

func TestPreflightRefusesOutputWithNoState(t *testing.T) {
	_, err := Preflight(PreflightInput{
		Dir:                compiled(t, ""),
		CurrentFingerprint: "anything",
	})
	if err == nil || !strings.Contains(err.Error(), StateFile) {
		t.Fatalf("output with no state file should be refused: %v", err)
	}

	warnings, err := Preflight(PreflightInput{
		Dir:                compiled(t, ""),
		CurrentFingerprint: "anything",
		Force:              true,
	})
	if err != nil {
		t.Fatalf("--force should allow it: %v", err)
	}
	if len(warnings) == 0 {
		t.Error("--force should warn about the missing state file")
	}
}

// TestNoFingerprintSkipsTheCheck covers the case where the source is not
// available to recompile: the deploy should proceed rather than refuse
// something it cannot actually judge.
func TestNoFingerprintSkipsTheCheck(t *testing.T) {
	_, err := Preflight(PreflightInput{Dir: compiled(t, "whatever")})
	if err != nil {
		t.Fatalf("an unknown current fingerprint should not block a deploy: %v", err)
	}
}

func TestStackNameDefaultsToTheApp(t *testing.T) {
	if got := (Options{App: "petstore"}).StackName(); got != "petstore" {
		t.Errorf("StackName = %q", got)
	}
	if got := (Options{App: "petstore", Stack: "prod"}).StackName(); got != "prod" {
		t.Errorf("StackName = %q", got)
	}
}

func TestDescribeStackSaysWhereItIsGoing(t *testing.T) {
	emulator := DescribeStack(MinistackStack, "http://localhost:4566")
	if !strings.Contains(emulator, "emulator") || !strings.Contains(emulator, "4566") {
		t.Errorf("an emulator stack should say so and name the endpoint: %q", emulator)
	}
	real := DescribeStack("prod", "")
	if !strings.Contains(real, "real AWS") {
		t.Errorf("a real deploy should say so plainly: %q", real)
	}
}

func TestEmulatorEnvIsolatesState(t *testing.T) {
	t.Setenv("PULUMI_BACKEND_URL", "")
	t.Setenv("PULUMI_ACCESS_TOKEN", "")
	t.Setenv("PULUMI_CONFIG_PASSPHRASE", "")

	env := emulatorEnv(Options{Dir: "/tmp/out", EmulatorEndpoint: "http://localhost:4566"})
	if got := env["PULUMI_BACKEND_URL"]; !strings.HasSuffix(got, LocalStateDir) {
		t.Errorf("an emulator deploy should keep state locally, got %q", got)
	}
	if env["PULUMI_CONFIG_PASSPHRASE"] == "" {
		t.Error("the local backend needs a passphrase, which cc should supply for an emulator stack")
	}
	if env["CC_AWS_ENDPOINT_URL"] != "http://localhost:4566" {
		t.Errorf("the packaging scripts need the endpoint: %q", env["CC_AWS_ENDPOINT_URL"])
	}
}

func TestEmulatorEnvRespectsAnExistingBackend(t *testing.T) {
	t.Setenv("PULUMI_BACKEND_URL", "s3://my-state")
	t.Setenv("PULUMI_CONFIG_PASSPHRASE", "mine")

	env := emulatorEnv(Options{Dir: "/tmp/out", EmulatorEndpoint: "http://localhost:4566"})
	if _, overridden := env["PULUMI_BACKEND_URL"]; overridden {
		t.Error("a backend the user configured must not be overridden")
	}
	if _, overridden := env["PULUMI_CONFIG_PASSPHRASE"]; overridden {
		t.Error("a passphrase the user configured must not be overridden")
	}
}

func TestARealDeployGetsNoEmulatorEnv(t *testing.T) {
	if env := emulatorEnv(Options{Dir: "/tmp/out"}); len(env) != 0 {
		t.Errorf("a real deploy should not be redirected anywhere: %v", env)
	}
}

func TestEmulatedServicesCoverWhatIsResolved(t *testing.T) {
	// Every service the resolver can emit must be redirected, or that resource
	// would quietly go to real AWS during an emulator deploy.
	for _, want := range []string{
		"dynamodb", "s3", "sns", "lambda", "apigatewayv2", "secretsmanager",
		"iam", "sts", "rds", "elasticache", "memorydb", "ecs", "ecr", "ec2", "elbv2",
	} {
		found := false
		for _, service := range EmulatedServices {
			if service == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s is resolvable but not redirected to the emulator", want)
		}
	}
}
