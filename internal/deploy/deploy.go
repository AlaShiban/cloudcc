// Package deploy drives the emitted Pulumi project through the Automation
// API.
//
// This is the only part of cloudcc that touches the network, and it is deliberately
// isolated: nothing on the compile path imports it, which keeps both the
// Automation API's weight and any possibility of a network call out of
// `cloudcc compile`. A structural test enforces that.
package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optdestroy"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optpreview"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optup"
)

// Action is what a deploy run should do.
type Action string

const (
	// ActionUp creates or updates the stack.
	ActionUp Action = "up"
	// ActionPreview shows what would change without changing it.
	ActionPreview Action = "preview"
	// ActionDestroy removes everything the stack created.
	ActionDestroy Action = "destroy"
)

// EmulatorStack is the stack name that auto-configures for an AWS emulator.
const EmulatorStack = "local"

// Options controls one deploy run.
type Options struct {
	// Dir is the compiled output directory.
	Dir string
	// App is the application name, used as the default stack name.
	App string
	// Stack overrides the stack name.
	Stack string
	// Action selects up, preview or destroy.
	Action Action
	// Force skips the fingerprint check (D19).
	Force bool
	// EmulatorEndpoint, when set, configures the stack against an
	// AWS-compatible emulator instead of real AWS.
	EmulatorEndpoint string
	// Region is the AWS region to deploy into.
	Region string
	// Out and Err receive Pulumi's streamed output.
	Out io.Writer
	Err io.Writer
	// SkipPackaging skips running the generated packaging script.
	SkipPackaging bool
}

// StackName returns the stack this run targets.
func (o Options) StackName() string {
	if o.Stack != "" {
		return o.Stack
	}
	return o.App
}

// Run performs the requested action.
func Run(ctx context.Context, opts Options) error {
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	if opts.Err == nil {
		opts.Err = os.Stderr
	}
	if opts.Region == "" {
		opts.Region = "us-east-1"
	}
	if opts.EmulatorEndpoint != "" {
		// The state directory must exist before Pulumi is pointed at it.
		if err := os.MkdirAll(filepath.Join(opts.Dir, LocalStateDir), 0o755); err != nil {
			return err
		}
	}

	if err := ensureDependencies(ctx, opts); err != nil {
		return err
	}

	stack, err := selectStack(ctx, opts)
	if err != nil {
		return err
	}

	if opts.EmulatorEndpoint != "" {
		if err := configureEmulator(ctx, stack, opts); err != nil {
			return err
		}
	}

	// Packaging runs before preview as well as before up: the generated program
	// references each unit's artefact by path, so a preview against a tree that
	// was never packaged fails on a missing file rather than showing a plan.
	if opts.Action != ActionDestroy && !opts.SkipPackaging {
		if err := runScript(ctx, opts, PackageScriptPath, "packaging execution units"); err != nil {
			return err
		}
	}

	switch opts.Action {
	case ActionPreview:
		_, err := stack.Preview(ctx,
			optpreview.ProgressStreams(opts.Out),
			optpreview.ErrorProgressStreams(opts.Err))
		return err

	case ActionDestroy:
		_, err := stack.Destroy(ctx,
			optdestroy.ProgressStreams(opts.Out),
			optdestroy.ErrorProgressStreams(opts.Err),
			optdestroy.Remove())
		return err

	case ActionUp:
		up := []optup.Option{
			optup.ProgressStreams(opts.Out),
			optup.ErrorProgressStreams(opts.Err),
		}
		if opts.EmulatorEndpoint != "" {
			// One resource at a time, against an emulator only.
			//
			// Emulators stand services up in-process as they are asked for, and
			// two of the same kind arriving together can collide in ways the
			// real API never does. LocalStack creating two RDS instances at
			// once is the case that forced this: both generate a TLS
			// certificate for their Postgres proxy at the same moment and one
			// fails with `[SSL] PEM lib`, which reaches Pulumi as the instance
			// entering state `error` with no reason attached. It is timing
			// dependent -- the same stack succeeds about as often as it fails
			// -- which makes it worse than a plain bug, because a suite that
			// passes four times in five looks like a flaky test rather than a
			// deployment nobody can trust.
			//
			// The cost is wall-clock on the emulator, which is where nobody is
			// waiting. A real deployment is untouched.
			up = append(up, optup.Parallel(1))
		}
		result, err := stack.Up(ctx, up...)
		if err != nil {
			return err
		}
		// Images can only be pushed once the registries exist, which is why
		// this runs after `up` rather than alongside packaging.
		if !opts.SkipPackaging && hasScript(opts.Dir, PushScriptPath) {
			if err := runScript(ctx, opts, PushScriptPath, "pushing container images"); err != nil {
				return err
			}
		}
		printOutputs(opts.Out, result.Outputs)
		return nil
	}
	return fmt.Errorf("unknown deploy action %q", opts.Action)
}

// ensureDependencies installs the generated project's Node dependencies.
//
// Pulumi refuses to run a program whose SDK is not installed, and the generated
// project is freshly written on every compile, so this is the common case
// rather than an edge one.
func ensureDependencies(ctx context.Context, opts Options) error {
	if _, err := os.Stat(filepath.Join(opts.Dir, "node_modules", "@pulumi", "pulumi")); err == nil {
		return nil
	}
	fmt.Fprintln(opts.Err, "cloudcc: installing the generated project's dependencies")

	cmd := exec.CommandContext(ctx, "pulumi", "install")
	cmd.Dir = opts.Dir
	cmd.Stdout = opts.Err // progress, not output the caller asked for
	cmd.Stderr = opts.Err
	cmd.Env = append(os.Environ(), "PULUMI_SKIP_UPDATE_CHECK=true")
	for k, v := range emulatorEnv(opts) {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("installing the generated project's dependencies failed: %w\n"+
			"  try running `npm install` in %s", err, opts.Dir)
	}
	return nil
}

// Script paths inside the compiled output, relative to its root.
const (
	PackageScriptPath = "bin/package.sh"
	PushScriptPath    = "bin/push-images.sh"
)

// LocalStateDir is where an emulator stack keeps its Pulumi state, inside the
// output directory.
const LocalStateDir = ".pulumi-state"

func selectStack(ctx context.Context, opts Options) (auto.Stack, error) {
	name := opts.StackName()

	var envOpts []auto.LocalWorkspaceOption
	if env := emulatorEnv(opts); len(env) > 0 {
		envOpts = append(envOpts, auto.EnvVars(env))
	}

	stack, err := auto.UpsertStackLocalSource(ctx, name, opts.Dir, envOpts...)
	if err != nil {
		return auto.Stack{}, fmt.Errorf("selecting stack %q in %s: %w", name, opts.Dir, err)
	}
	return stack, nil
}

// emulatorEnv makes an emulator deploy work without a Pulumi account.
//
// State goes to a directory inside the output, and the local backend's
// passphrase is fixed: there is nothing secret in a stack that only ever
// talked to a local emulator, and requiring the user to invent a passphrase
// for it would be friction with no benefit.
func emulatorEnv(opts Options) map[string]string {
	if opts.EmulatorEndpoint == "" {
		return nil
	}
	env := map[string]string{}
	if os.Getenv("PULUMI_BACKEND_URL") == "" && os.Getenv("PULUMI_ACCESS_TOKEN") == "" {
		env["PULUMI_BACKEND_URL"] = "file://" + filepath.Join(opts.Dir, LocalStateDir)
	}
	if os.Getenv("PULUMI_CONFIG_PASSPHRASE") == "" && os.Getenv("PULUMI_CONFIG_PASSPHRASE_FILE") == "" {
		env["PULUMI_CONFIG_PASSPHRASE"] = "cloudcc-emulator"
	}
	env["CLOUDCC_AWS_ENDPOINT_URL"] = opts.EmulatorEndpoint
	return env
}

// configureEmulator points a stack at an AWS-compatible emulator.
//
// Every setting here exists because the emulator is not real AWS: credentials
// cannot be validated, there is no instance metadata service and no account to
// look up, and S3 must be addressed path-style.
func configureEmulator(ctx context.Context, stack auto.Stack, opts Options) error {
	settings := []struct {
		key   string
		value string
		path  bool
	}{
		{key: "aws:region", value: opts.Region},
		{key: "aws:accessKey", value: emulatorCredential("AWS_ACCESS_KEY_ID")},
		{key: "aws:secretKey", value: emulatorCredential("AWS_SECRET_ACCESS_KEY")},
		{key: "aws:skipCredentialsValidation", value: "true"},
		{key: "aws:skipMetadataApiCheck", value: "true"},
		// Not skipped. Skipping it leaves the provider with an empty account
		// id, which some emulators ignore and LocalStack does not: SNS answers
		// GetTopicAttributes with "'' is not a valid AWS account ID" and the
		// stack dies on its first topic. Every emulator answers STS, so there
		// was never anything to skip.
		{key: "aws:skipRequestingAccountId", value: "false"},
		{key: "aws:s3UsePathStyle", value: "true"},
	}
	for _, service := range EmulatedServices {
		settings = append(settings, struct {
			key   string
			value string
			path  bool
		}{
			key:   fmt.Sprintf("aws:endpoints[0].%s", service),
			value: opts.EmulatorEndpoint,
			path:  true,
		})
	}

	for _, s := range settings {
		// Plaintext throughout: these are throwaway emulator settings, and
		// Pulumi otherwise refuses keys whose names look secret.
		value := auto.ConfigValue{Value: s.value, Secret: false}
		if err := stack.SetConfigWithOptions(ctx, s.key, value,
			&auto.ConfigOptions{Path: s.path}); err != nil {
			return fmt.Errorf("configuring %s: %w", s.key, err)
		}
	}
	return nil
}

// EmulatedServices are the AWS services pointed at the emulator endpoint.
// A service the resolver can emit and this list omits is not a degraded
// deploy: the provider sends that one call to the real AWS endpoint, which
// answers InvalidClientTokenId to the emulator's throwaway credentials and
// takes the whole stack down. Adding a resource type means adding its service
// here, which is a rule a test enforces rather than a rule to remember.
var EmulatedServices = []string{
	"apigateway", "apigatewayv2", "cloudfront", "cloudwatch", "cloudwatchlogs",
	"dynamodb", "ec2", "ecr", "ecs", "eks", "elasticache", "elbv2", "iam",
	"lambda", "logs", "memorydb", "rds", "s3", "secretsmanager", "sns", "sqs",
	"sts",
}

func emulatorCredential(env string) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	return "cloudcc-local"
}

func hasScript(dir, rel string) bool {
	info, err := os.Stat(filepath.Join(dir, rel))
	return err == nil && info.Mode().IsRegular()
}

func runScript(ctx context.Context, opts Options, rel, describe string) error {
	path := filepath.Join(opts.Dir, rel)
	if !hasScript(opts.Dir, rel) {
		return fmt.Errorf("%s is missing from %s; recompile before deploying", rel, opts.Dir)
	}
	fmt.Fprintf(opts.Err, "cloudcc: %s\n", describe)

	cmd := exec.CommandContext(ctx, "bash", path)
	cmd.Dir = opts.Dir
	cmd.Stdout = opts.Out
	cmd.Stderr = opts.Err
	cmd.Env = os.Environ()
	// The generated scripts talk to Pulumi themselves -- push-images.sh reads
	// the stack's outputs to find each repository -- and a separate process has
	// no idea which stack this is or where its state lives. The automation API
	// carries that in the workspace; a child gets it only if it is handed over.
	//
	// Without this the push fails with "no stack selected", a message about the
	// CLI's own workspace rather than about the deployment, and nothing noticed
	// because no container unit had ever been deployed.
	cmd.Env = append(cmd.Env, "CLOUDCC_STACK="+opts.Stack)
	for key, value := range emulatorEnv(opts) {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w", rel, err)
	}
	return nil
}

// printOutputs prints the stack outputs, with the environment bindings first,
// since those are what a caller most often wants.
func printOutputs(w io.Writer, outputs auto.OutputMap) {
	if len(outputs) == 0 {
		return
	}
	keys := make([]string, 0, len(outputs))
	for k := range outputs {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		ci, cj := strings.HasPrefix(keys[i], "CLOUDCC_"), strings.HasPrefix(keys[j], "CLOUDCC_")
		if ci != cj {
			return ci
		}
		return keys[i] < keys[j]
	})

	fmt.Fprintln(w, "\nOutputs:")
	for _, k := range keys {
		value := outputs[k].Value
		if outputs[k].Secret {
			fmt.Fprintf(w, "  %-40s [secret]\n", k)
			continue
		}
		fmt.Fprintf(w, "  %-40s %v\n", k, render(value))
	}
	fmt.Fprintf(w, "\nTo run a compiled unit locally against these resources:\n"+
		"  eval \"$(pulumi stack output --json | jq -r 'to_entries[]|select(.key|startswith(\"CLOUDCC_\"))|\"export \\(.key)=\\(.value|@sh)\"')\"\n")
}

func render(v any) string {
	switch typed := v.(type) {
	case string:
		return typed
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(data)
	}
}
