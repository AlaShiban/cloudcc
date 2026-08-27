package aws

import (
	"fmt"
	"sort"
	"strings"

	"github.com/cloudcompiler/cloudcc/internal/config"
)

// The `resources:` block: how an execution unit's size and behaviour are
// configured, in a spelling that is not tied to one infrastructure tool.
//
// # Why snake_case, and why these exact names
//
// Pulumi's AWS provider is code-generated from the Terraform AWS provider's
// schema. The two therefore describe the same resource with the same argument
// names, differing only in case: Terraform and OpenTofu write `memory_size`,
// Pulumi's TypeScript SDK writes `memorySize`, and Pulumi's Python and YAML
// SDKs write `memory_size` again. Nested Terraform blocks -- `ephemeral_storage
// { size = 2048 }` -- are nested objects in Pulumi.
//
// So the portable spelling is the Terraform one, and it is the *only* spelling
// this file accepts. A cloudcc.yaml written today emits Pulumi TypeScript
// through a mechanical case transform; when an OpenTofu backend arrives it will
// emit these names unchanged. Accepting `memorySize` would be accepting one
// backend's dialect into the user's configuration, and the whole point of
// having a configuration file rather than a Pulumi program is that the choice
// of backend is not the user's problem.
//
// # Why a table rather than a struct
//
// Three things have to stay in step per argument: the portable name, the Pulumi
// name, and what counts as a valid value. A table keeps them on one line, and
// makes the diagnostic for an unknown key a list of what is actually supported
// rather than a Go type name.
//
// # What is deliberately absent
//
// Everything the compiler owns. A unit's runtime, handler, role, code and
// environment are derived from its source and its declarations; letting a
// configuration file set them would produce a function whose code and whose
// declared entrypoint disagree, which fails at the first invocation with a
// message about neither. Those names are listed in ownedByCompiler so the error
// can say why rather than "unknown key".

// argKind is how a value is checked and rendered.
type argKind int

const (
	argInt argKind = iota
	argString
	argBool
	argStringList
	argBlock
)

// lambdaArg is one argument of aws_lambda_function / aws.lambda.Function.
type lambdaArg struct {
	// Name is the Terraform/OpenTofu argument name, and what cloudcc.yaml
	// writes.
	Name string
	// Pulumi is the same argument in Pulumi's TypeScript SDK.
	Pulumi string
	Kind   argKind
	// Fields are a block's arguments, for Kind == argBlock.
	Fields []lambdaArg
	// Check validates a scalar, returning a sentence that completes
	// "<name> ...". Nil means any value of the right type is accepted.
	Check func(any) string
	// Why is one line on what the argument does, shown when listing what is
	// supported.
	Why string
}

// lambdaArgs is the supported surface, sorted by name.
//
// Short on purpose. Every entry is an argument somebody has a reason to set on
// a function whose code cloudcc built, and each one is checked -- an argument
// that is merely passed through is one whose failure arrives from AWS at deploy
// time instead of from here at compile time.
var lambdaArgs = []lambdaArg{
	{
		Name: "architectures", Pulumi: "architectures", Kind: argStringList,
		Why: `the instruction set: ["arm64"] or ["x86_64"]`,
		Check: func(v any) string {
			list, _ := v.([]any)
			if len(list) != 1 {
				return "takes exactly one architecture; AWS Lambda supports one per function"
			}
			switch fmt.Sprint(list[0]) {
			case "arm64", "x86_64":
				return ""
			}
			return fmt.Sprintf("must be \"arm64\" or \"x86_64\", not %q", fmt.Sprint(list[0]))
		},
	},
	{
		Name: "description", Pulumi: "description", Kind: argString,
		Why: "free text shown in the console",
	},
	{
		Name: "ephemeral_storage", Pulumi: "ephemeralStorage", Kind: argBlock,
		Why: "how much space /tmp has",
		Fields: []lambdaArg{{
			Name: "size", Pulumi: "size", Kind: argInt,
			Why:   "megabytes of /tmp, 512 to 10240",
			Check: intBetween(512, 10240, "MB"),
		}},
	},
	{
		Name: "layers", Pulumi: "layers", Kind: argStringList,
		Why: "ARNs of Lambda layers to attach",
	},
	{
		Name: "memory_size", Pulumi: "memorySize", Kind: argInt,
		Why:   "megabytes of memory, 128 to 10240; CPU is allocated in proportion",
		Check: intBetween(128, 10240, "MB"),
	},
	{
		Name: "publish", Pulumi: "publish", Kind: argBool,
		Why: "publish a new numbered version on each deployment",
	},
	{
		Name: "reserved_concurrent_executions", Pulumi: "reservedConcurrentExecutions", Kind: argInt,
		Why: "cap on simultaneous executions; 0 stops the function entirely, -1 is no cap",
		Check: func(v any) string {
			n, ok := asInt(v)
			if !ok {
				return "must be a whole number"
			}
			if n < -1 {
				return "must be -1 (no reservation), 0 (throttled to a stop) or more"
			}
			return ""
		},
	},
	{
		Name: "snap_start", Pulumi: "snapStart", Kind: argBlock,
		Why: "restore from a snapshot of an initialised execution environment",
		Fields: []lambdaArg{{
			Name: "apply_on", Pulumi: "applyOn", Kind: argString,
			Why:   `"PublishedVersions" or "None"`,
			Check: oneOf("PublishedVersions", "None"),
		}},
	},
	{
		Name: "timeout", Pulumi: "timeout", Kind: argInt,
		Why:   "seconds before an invocation is killed, 1 to 900",
		Check: intBetween(1, 900, "seconds"),
	},
	{
		Name: "tracing_config", Pulumi: "tracingConfig", Kind: argBlock,
		Why: "X-Ray tracing",
		Fields: []lambdaArg{{
			Name: "mode", Pulumi: "mode", Kind: argString,
			Why:   `"Active" or "PassThrough"`,
			Check: oneOf("Active", "PassThrough"),
		}},
	},
}

// ownedByCompiler maps an argument the compiler derives to the reason it cannot
// be set here. Keyed by both spellings, because someone reaching for these has
// most likely copied them from a Pulumi program or a Terraform module.
var ownedByCompiler = map[string]string{
	"function_name": "a unit's function name comes from the app and the unit id, and every binding that points at this function is generated from it",
	"name":          "a unit's function name comes from the app and the unit id, and every binding that points at this function is generated from it",
	"runtime":       "the runtime comes from the language the unit is written in, and its bundle is built for that runtime",
	"handler":       "the handler is the generated entrypoint that adapts this unit's application to Lambda",
	"role":          "the role is generated from what the unit's code reaches for, which is how least privilege stays true without a list to maintain",
	"code":          "the code is the bundle the compiler packaged",
	"s3_bucket":     "the code is the bundle the compiler packaged",
	"s3_key":        "the code is the bundle the compiler packaged",
	"filename":      "the code is the bundle the compiler packaged",
	"image_uri":     "a unit deployed as an image is `type: ecs`, and its image is built and pushed by the generated project",
	"environment":   "a unit's environment is derived from its declarations; use `environment_variables:` on the unit to add to it",
	"vpc_config":    "the network comes from whether the unit reaches a resource that lives in one",
}

func intBetween(low, high int, unit string) func(any) string {
	return func(v any) string {
		n, ok := asInt(v)
		if !ok {
			return "must be a whole number"
		}
		if n < low || n > high {
			return fmt.Sprintf("must be between %d and %d %s; AWS rejects %d", low, high, unit, n)
		}
		return ""
	}
}

func oneOf(allowed ...string) func(any) string {
	return func(v any) string {
		s := fmt.Sprint(v)
		for _, a := range allowed {
			if s == a {
				return ""
			}
		}
		return fmt.Sprintf("must be one of %s, not %q", quotedList(allowed), s)
	}
}

// asInt accepts the several shapes a YAML number arrives as.
func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		// YAML gives 1024 as a float only when it was written 1024.0, which is
		// not a memory size anybody means.
		if n != float64(int(n)) {
			return 0, false
		}
		return int(n), true
	}
	return 0, false
}

// LambdaResourceArgs validates a unit's `resources:` block and returns it in
// the spelling the Pulumi backend emits.
//
// The returned map is empty when nothing was configured, which is the ordinary
// case: the defaults in compute.go stand.
func LambdaResourceArgs(unitID string, cfg config.ResourceConfig) (map[string]any, error) {
	if len(cfg.Resources) == 0 {
		return nil, nil
	}
	out, err := translate(unitID, "", cfg.Resources, lambdaArgs)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// translate walks one level of a resources block against its schema.
func translate(unitID, path string, values map[string]any, schema []lambdaArg) (map[string]any, error) {
	byName := map[string]lambdaArg{}
	for _, a := range schema {
		byName[a.Name] = a
	}

	// Sorted so the first error a user sees does not depend on map ordering
	// (D18 applies to diagnostics as much as to output).
	names := make([]string, 0, len(values))
	for k := range values {
		names = append(names, k)
	}
	sort.Strings(names)

	out := map[string]any{}
	for _, name := range names {
		value := values[name]
		arg, ok := byName[name]
		if !ok {
			return nil, unknownArg(unitID, path, name, schema)
		}
		full := name
		if path != "" {
			full = path + "." + name
		}

		if arg.Kind == argBlock {
			nested, ok := asStringMap(value)
			if !ok {
				return nil, fmt.Errorf("execution unit %q: resources.%s takes a block of settings, not a single value",
					unitID, full)
			}
			inner, err := translate(unitID, full, nested, arg.Fields)
			if err != nil {
				return nil, err
			}
			out[arg.Pulumi] = inner
			continue
		}

		converted, err := scalar(unitID, full, arg, value)
		if err != nil {
			return nil, err
		}
		if arg.Check != nil {
			if why := arg.Check(converted); why != "" {
				return nil, fmt.Errorf("execution unit %q: resources.%s %s", unitID, full, why)
			}
		}
		out[arg.Pulumi] = converted
	}
	return out, nil
}

// scalar checks a value's shape and normalises it for the renderer.
func scalar(unitID, full string, arg lambdaArg, value any) (any, error) {
	wrong := func(want string) error {
		return fmt.Errorf("execution unit %q: resources.%s must be %s, not %s -- %s",
			unitID, full, want, describeValue(value), arg.Why)
	}
	switch arg.Kind {
	case argInt:
		n, ok := asInt(value)
		if !ok {
			return nil, wrong("a whole number")
		}
		return n, nil
	case argString:
		s, ok := value.(string)
		if !ok {
			return nil, wrong("text")
		}
		return s, nil
	case argBool:
		b, ok := value.(bool)
		if !ok {
			return nil, wrong("true or false")
		}
		return b, nil
	case argStringList:
		list, ok := value.([]any)
		if !ok {
			return nil, wrong("a list")
		}
		for _, item := range list {
			if _, ok := item.(string); !ok {
				return nil, wrong("a list of text")
			}
		}
		return list, nil
	}
	return value, nil
}

// unknownArg builds the diagnostic for a name that is not in the schema.
func unknownArg(unitID, path, name string, schema []lambdaArg) error {
	where := "resources"
	if path != "" {
		where = "resources." + path
	}
	if why, owned := ownedByCompiler[name]; owned && path == "" {
		return fmt.Errorf("execution unit %q: %s.%s cannot be set here: %s",
			unitID, where, name, why)
	}

	var lines []string
	for _, a := range schema {
		lines = append(lines, fmt.Sprintf("    %-30s %s", a.Name, a.Why))
	}
	hint := ""
	if suggestion := closestArg(name, schema); suggestion != "" {
		hint = fmt.Sprintf(" Did you mean %q?", suggestion)
	}
	return fmt.Errorf("execution unit %q: %s has no argument %q.%s\n  Arguments are spelled the way OpenTofu and Terraform spell them, which is\n  also how Pulumi's Python and YAML SDKs spell them. These are supported:\n%s",
		unitID, where, name, hint, strings.Join(lines, "\n"))
}

// closestArg suggests a schema name for a near miss, which is nearly always a
// camelCase spelling copied out of a Pulumi program.
func closestArg(name string, schema []lambdaArg) string {
	flat := strings.ToLower(strings.ReplaceAll(name, "_", ""))
	for _, a := range schema {
		if strings.ToLower(strings.ReplaceAll(a.Name, "_", "")) == flat {
			return a.Name
		}
	}
	return ""
}

func asStringMap(v any) (map[string]any, bool) {
	switch m := v.(type) {
	case map[string]any:
		return m, true
	case map[any]any:
		out := make(map[string]any, len(m))
		for k, val := range m {
			out[fmt.Sprint(k)] = val
		}
		return out, true
	}
	return nil, false
}

func describeValue(v any) string {
	switch v.(type) {
	case nil:
		return "nothing"
	case string:
		return "text"
	case bool:
		return "true or false"
	case []any:
		return "a list"
	case map[string]any, map[any]any:
		return "a block"
	}
	return fmt.Sprintf("%v", v)
}

// quotedList renders names as `"a", "b" or "c"`, for a diagnostic.
func quotedList(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = fmt.Sprintf("%q", n)
	}
	switch len(quoted) {
	case 0:
		return ""
	case 1:
		return quoted[0]
	}
	return strings.Join(quoted[:len(quoted)-1], ", ") + " or " + quoted[len(quoted)-1]
}

// CheckResourcesAreSupported rejects a `resources:` block on a declaration that
// has nowhere to put it.
//
// Only Lambda execution units read one today. A block written on an ECS unit,
// a table or a topic would otherwise be accepted by the loader, resolved into
// nothing, and never mentioned again -- so the configuration file would say
// something the deployment does not do, which is the failure this project
// refuses everywhere else. Saying so costs one sentence and saves reading a
// stack to find out a setting was ignored.
func CheckResourcesAreSupported(app *config.App) error {
	type section struct {
		label   string
		entries map[string]config.ResourceConfig
	}
	sections := []section{
		{"exposed", app.Exposed},
		{"persisted", app.Persisted},
		{"pubsub", app.PubSub},
		{"static_units", app.StaticUnits},
		{"config", app.ConfigVars},
	}

	for _, s := range sections {
		ids := make([]string, 0, len(s.entries))
		for id := range s.entries {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			if len(s.entries[id].Resources) > 0 {
				return fmt.Errorf("%s %q: `resources:` is only supported on execution units of "+
					"type lambda. Nothing would read it here, so it would be a setting that "+
					"looks applied and is not; use `pulumi_params:` if you need to reach an "+
					"argument cloudcc does not model", s.label, id)
			}
		}
	}

	units := make([]string, 0, len(app.ExecutionUnits))
	for id := range app.ExecutionUnits {
		units = append(units, id)
	}
	sort.Strings(units)
	for _, id := range units {
		unit := app.ExecutionUnits[id]
		if len(unit.Resources) == 0 {
			continue
		}
		// Resolved rather than as-written: the type may come from a defaults
		// layer rather than from the unit's own entry.
		if resolved := app.Lookup(config.KindExecutionUnit, id).Type; resolved != "lambda" {
			return fmt.Errorf("execution unit %q is type %q, and `resources:` is only supported "+
				"on type lambda. The arguments differ by resource -- a Fargate service is sized "+
				"in cpu and memory units, not in megabytes of function memory -- so accepting "+
				"these here would mean accepting settings that cannot be applied", id, resolved)
		}
	}
	return nil
}
