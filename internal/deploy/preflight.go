package deploy

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// StateFile records what the last compile produced.
const StateFile = ".cloudcc-state.json"

// State is the fingerprint record written next to the generated project.
type State struct {
	Version     int      `json:"version"`
	App         string   `json:"app"`
	Fingerprint string   `json:"fingerprint"`
	Units       []string `json:"units"`
}

// ReadState loads the state file from a compiled output directory.
func ReadState(dir string) (State, error) {
	var st State
	data, err := os.ReadFile(filepath.Join(dir, StateFile))
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, fmt.Errorf("parsing %s: %w", StateFile, err)
	}
	return st, nil
}

// PreflightInput is what Preflight needs to check a deploy.
type PreflightInput struct {
	// Dir is the compiled output directory.
	Dir string
	// CurrentFingerprint is the fingerprint of a fresh compile of the source.
	// Empty means the caller could not compute one, and the check is skipped.
	CurrentFingerprint string
	// RequireCredentials is true for a real-AWS deploy.
	RequireCredentials bool
	// Force downgrades the stale-output refusal to a warning.
	Force bool
}

// PreflightError is a refusal with an actionable message.
type PreflightError struct {
	Reason string
	Fix    string
}

func (e *PreflightError) Error() string {
	if e.Fix == "" {
		return e.Reason
	}
	return e.Reason + "\n  " + e.Fix
}

// Preflight checks that a deploy can succeed before anything is changed.
//
// The fingerprint check is the important one (D19): deploying output that no
// longer matches the source is how a stack quietly diverges from the program
// that was supposed to have produced it.
func Preflight(in PreflightInput) ([]string, error) {
	var warnings []string

	info, err := os.Stat(in.Dir)
	if err != nil || !info.IsDir() {
		return nil, &PreflightError{
			Reason: fmt.Sprintf("no compiled output at %s", in.Dir),
			Fix:    "run `cloudcc compile <path>` first",
		}
	}
	if _, err := os.Stat(filepath.Join(in.Dir, "index.ts")); err != nil {
		return nil, &PreflightError{
			Reason: fmt.Sprintf("%s does not look like a compiled project (no index.ts)", in.Dir),
			Fix:    "run `cloudcc compile <path>` first, or pass -o to point at the right directory",
		}
	}
	if _, err := exec.LookPath("pulumi"); err != nil {
		return nil, &PreflightError{
			Reason: "the pulumi CLI is not installed",
			Fix:    "install it with: brew install pulumi",
		}
	}
	if _, err := exec.LookPath("uv"); err != nil {
		warnings = append(warnings, "uv is not installed, so packaging will fail (brew install uv)")
	}

	state, err := ReadState(in.Dir)
	switch {
	case os.IsNotExist(err):
		if !in.Force {
			return warnings, &PreflightError{
				Reason: fmt.Sprintf("%s has no %s, so cloudcc cannot tell whether it matches your source", in.Dir, StateFile),
				Fix:    "recompile, or pass --force to deploy it anyway",
			}
		}
		warnings = append(warnings, "deploying output with no state file because --force was given")
	case err != nil:
		return warnings, &PreflightError{Reason: err.Error(), Fix: "recompile to regenerate it"}
	case in.CurrentFingerprint != "" && state.Fingerprint != in.CurrentFingerprint:
		if !in.Force {
			return warnings, &PreflightError{
				Reason: fmt.Sprintf(
					"the compiled output in %s is stale: it was produced from a different source or configuration\n"+
						"    output:  %s\n    current: %s",
					in.Dir, short(state.Fingerprint), short(in.CurrentFingerprint)),
				Fix: "recompile, or pass --force to deploy the existing output anyway",
			}
		}
		warnings = append(warnings, "deploying stale output because --force was given")
	}

	if in.RequireCredentials && !credentialsPresent() {
		return warnings, &PreflightError{
			Reason: "no AWS credentials found",
			Fix: "configure them with `aws configure`, or deploy against an emulator with --stack " +
				MinistackStack,
		}
	}
	return warnings, nil
}

func short(fingerprint string) string {
	if len(fingerprint) > 12 {
		return fingerprint[:12]
	}
	if fingerprint == "" {
		return "(none)"
	}
	return fingerprint
}

func credentialsPresent() bool {
	for _, env := range []string{"AWS_ACCESS_KEY_ID", "AWS_PROFILE", "AWS_ROLE_ARN"} {
		if os.Getenv(env) != "" {
			return true
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	for _, name := range []string{"credentials", "config"} {
		if _, err := os.Stat(filepath.Join(home, ".aws", name)); err == nil {
			return true
		}
	}
	return false
}

// DescribeStack explains what a stack name implies, for the deploy summary.
func DescribeStack(name, endpoint string) string {
	if strings.EqualFold(name, MinistackStack) {
		return fmt.Sprintf("stack %q, configured against the emulator at %s", name, endpoint)
	}
	return fmt.Sprintf("stack %q, against real AWS", name)
}
