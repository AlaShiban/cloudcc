// Package config defines cloudcc.yaml: the single, layered configuration file that
// drives every type-selection decision the compiler makes (D5).
//
// Layering, from weakest to strongest:
//
//	defaults.<kind>                 -- provider default-by-kind
//	defaults.<kind>.by_type.<type>  -- provider default-by-type
//	<section>.<id>                  -- explicit per-resource
//
// The fully-resolved result is written back out to compiled/cloudcc.yaml so that
// every inferred decision is inspectable.
package config

import (
	"bytes"
	"fmt"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Capability kinds. These are the canonical names used as graph node kinds,
// diagnostic prefixes, and `defaults` keys.
const (
	KindExecutionUnit = "execution_unit"
	KindExpose        = "expose"
	KindPersistKV     = "persist_kv"
	KindPersistFS     = "persist_fs"
	KindPersistSecret = "persist_secret"
	KindPersistORM    = "persist_orm"
	KindPersistRedis  = "persist_redis"
	KindPubSub        = "pubsub"
	KindStaticUnit    = "static_unit"
	KindConfig        = "config"
	// KindLogging is where a unit's logs go. Unlike every other kind it is
	// declared in configuration rather than in code: a program does not choose
	// its log destination, an operator does, and the call sites are identical
	// either way.
	KindLogging = "logging"
)

// Compute types for an execution unit.
//
// Named for what they are rather than for what one cloud calls them. `function`
// is AWS Lambda, Azure Functions or GCP Cloud Functions; `container` is ECS
// Fargate, Azure Container Apps or Cloud Run. The provider chooses; the program
// says which shape of compute it needs, which is a decision that survives
// changing cloud.
//
// This is the top of a two-layer scheme. `type:` and the portable settings
// beside it say what a unit is and how big; a block named after a provider
// resource -- `aws.lambda.Function` -- says everything a specific cloud offers
// beyond that. The line at which portability stops is therefore visible in the
// file rather than something a reader has to know argument by argument.
const (
	TypeFunction  = "function"
	TypeContainer = "container"
)

// Platforms a container unit can run on.
//
// A second axis, and portable for the same reason `type:` is. "Run my container
// on Kubernetes" means something on all three clouds -- EKS, GKE, AKS -- and so
// does "run it on the cloud's own container service", which is Fargate here and
// Cloud Run or Container Apps elsewhere. What is not portable is the product
// name, so neither value is one.
//
// Keeping this off the `type:` axis is what lets `memory:` go on meaning one
// thing. A unit is a container either way; where it runs is a separate
// question, and answering it does not change what the unit is.
const (
	PlatformServerless = "serverless"
	PlatformKubernetes = "kubernetes"
)

// Platforms is the canonical list, for diagnostics.
var Platforms = []string{PlatformKubernetes, PlatformServerless}

// CheckPlatform reports a platform that is not one of the two, or one written
// on a compute type that has no such choice.
func CheckPlatform(id, computeType, platform string) error {
	if platform == "" {
		return nil
	}
	if computeType != TypeContainer {
		return fmt.Errorf("execution unit %q is type %q, and `platform:` only applies to type "+
			"%q. A function is run by the provider's function service and there is no second "+
			"way to run it; a container has to say whether it wants Kubernetes or the cloud's "+
			"own container service", id, computeType, TypeContainer)
	}
	for _, known := range Platforms {
		if platform == known {
			return nil
		}
	}
	return fmt.Errorf("execution unit %q: no platform %q. It is %q -- EKS, GKE or AKS -- or "+
		"%q, which is the provider's own container service and the default",
		id, platform, PlatformKubernetes, PlatformServerless)
}

// renamedTypes maps a compute type that used to be spelled after one cloud's
// product onto what it is called now.
//
// An error rather than a silent alias. Two spellings for one thing is exactly
// what this scheme exists to remove, and a configuration file that still says
// `lambda` is one whose author has not yet been told that the name now means
// something portable.
var renamedTypes = map[string]struct{ now, commonality string }{
	"lambda": {TypeFunction, "AWS Lambda, Azure Functions and GCP Cloud Functions"},
	"ecs":    {TypeContainer, "ECS Fargate, Azure Container Apps and Cloud Run"},
}

// CheckComputeType reports a compute type that has been renamed.
func CheckComputeType(id, typ string) error {
	if renamed, ok := renamedTypes[typ]; ok {
		return fmt.Errorf("execution unit %q: `type: %s` is now `type: %s`. The type says "+
			"what shape of compute a unit needs, not which product runs it -- %q is what "+
			"%s have in common, and `provider:` picks between them. Anything specific to "+
			"one of them goes in a block named after the resource, such as "+
			"`aws.lambda.Function:`",
			id, typ, renamed.now, renamed.now, renamed.commonality)
	}
	return nil
}

// Kinds is the canonical, sorted list of capability kinds.
var Kinds = []string{
	KindConfig,
	KindExecutionUnit,
	KindExpose,
	KindPersistFS,
	KindPersistKV,
	KindPersistORM,
	KindPersistRedis,
	KindPersistSecret,
	KindLogging,
	KindPubSub,
	KindStaticUnit,
}

// IsKind reports whether name is a known capability kind.
func IsKind(name string) bool {
	for _, k := range Kinds {
		if k == name {
			return true
		}
	}
	return false
}

// IsPersistKind reports whether kind is one of the persist_* family, all of
// which share the `persisted` config section.
func IsPersistKind(kind string) bool {
	return strings.HasPrefix(kind, "persist_")
}

// ResourceConfig is the per-resource configuration record. Every field is
// optional; Merge implements "the stronger layer wins, maps deep-merge".
type ResourceConfig struct {
	// Type selects the concrete provider resource, e.g. "dynamodb".
	Type string `yaml:"type,omitempty" json:"type,omitempty"`
	// EnvironmentVariables are extra env vars for an execution unit.
	EnvironmentVariables map[string]string `yaml:"environment_variables,omitempty" json:"environment_variables,omitempty"`
	// Secret marks a config value as a Pulumi stack secret (D21).
	Secret bool `yaml:"secret,omitempty" json:"secret,omitempty"`
	// Value is the literal default for a config value.
	Value string `yaml:"value,omitempty" json:"value,omitempty"`
	// StaticFiles / SharedFiles / IndexDocument configure a static unit.
	// RetentionDays is how long logs are kept. Zero means the default.
	RetentionDays int `yaml:"retention_days,omitempty" json:"retention_days,omitempty"`

	StaticFiles   string `yaml:"static_files,omitempty" json:"static_files,omitempty"`
	SharedFiles   string `yaml:"shared_files,omitempty" json:"shared_files,omitempty"`
	IndexDocument string `yaml:"index_document,omitempty" json:"index_document,omitempty"`
	// Resources holds provider arguments for the resource this declaration
	// becomes -- how big it is, how long it may run, what it is allowed to use.
	//
	// Spelled the way OpenTofu and Terraform spell them, which is also how
	// Pulumi's Python and YAML SDKs spell them, because Pulumi's AWS provider
	// is generated from Terraform's and the two describe the same arguments.
	// Emitting Pulumi TypeScript is a case transform; emitting OpenTofu later
	// is the identity. Choosing one backend's dialect here would put the choice
	// of backend into every user's configuration file.
	//
	// Held untyped because which arguments are legal depends on what the
	// declaration resolves to, which is the provider's business (D7). The
	// provider validates it and reports an unknown or out-of-range argument as
	// a compile error naming what is supported.
	// Memory is how many megabytes the compute gets, and Timeout how many
	// seconds it may run for. These two are the portable layer: AWS Lambda,
	// Azure Functions and GCP Cloud Functions each have an answer for both, so
	// a program that sets them is not saying anything about which cloud it
	// runs on.
	//
	// Deliberately only two. An abstraction designed against one implementation
	// comes out shaped like that implementation, and the tempting third
	// candidates do not survive the comparison: `architectures` is effectively
	// AWS-only today, `cpu` cannot be honoured on Lambda at all because CPU is
	// allocated in proportion to memory, and "concurrency" names a reservation
	// from a shared account pool on AWS and a plain instance ceiling on GCP --
	// the same word with a different blast radius. Those live in the provider
	// layer below until a second provider proves they generalise.
	Memory int `yaml:"memory,omitempty" json:"memory,omitempty"`
	// Platform is where a container runs: `kubernetes` or `serverless`. Empty
	// means the default, which is the provider's own container service.
	Platform string `yaml:"platform,omitempty" json:"platform,omitempty"`
	Timeout  int    `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	// ProviderArgs holds arguments for a specific provider resource, keyed by
	// that resource's type -- `aws.lambda.Function`. This is the second layer:
	// everything the portable one deliberately does not model.
	//
	// Untyped because which arguments are legal depends on the resource, which
	// is the provider's business (D7). The provider validates it and reports an
	// unknown or out-of-range argument as a compile error naming what is
	// supported.
	//
	// Populated by UnmarshalYAML from any key containing a dot, which is what
	// distinguishes a resource type from one of the fields above.
	ProviderArgs map[string]map[string]any `yaml:"-" json:"provider_args,omitempty"`
	// PulumiParams is the escape hatch: deep-merged into the generated Pulumi
	// resource args for this resource, in Pulumi's own spelling and with no
	// checking at all.
	//
	// Distinct from Resources on purpose. This one reaches arguments cloudcc
	// has no opinion about, at the cost of naming them the way one backend
	// happens to name them -- so a project that uses it is a project that has
	// to revisit its configuration if the backend changes. Prefer Resources.
	PulumiParams map[string]any `yaml:"pulumi_params,omitempty" json:"pulumi_params,omitempty"`
}

// Merge returns a copy of rc with the non-zero fields of other layered on top.
// other is the stronger layer.
func (rc ResourceConfig) Merge(other ResourceConfig) ResourceConfig {
	out := rc
	if other.Type != "" {
		out.Type = other.Type
	}
	if other.Secret {
		out.Secret = true
	}
	if other.Value != "" {
		out.Value = other.Value
	}
	if other.StaticFiles != "" {
		out.StaticFiles = other.StaticFiles
	}
	if other.SharedFiles != "" {
		out.SharedFiles = other.SharedFiles
	}
	if other.IndexDocument != "" {
		out.IndexDocument = other.IndexDocument
	}
	if other.RetentionDays != 0 {
		out.RetentionDays = other.RetentionDays
	}
	out.EnvironmentVariables = mergeStringMap(rc.EnvironmentVariables, other.EnvironmentVariables)
	if other.Memory != 0 {
		out.Memory = other.Memory
	}
	if other.Platform != "" {
		out.Platform = other.Platform
	}
	if other.Timeout != 0 {
		out.Timeout = other.Timeout
	}
	out.ProviderArgs = mergeProviderArgs(rc.ProviderArgs, other.ProviderArgs)
	out.PulumiParams = DeepMerge(rc.PulumiParams, other.PulumiParams)
	return out
}

// Architecture is the instruction set a compute declaration asked for, or ""
// when it did not.
//
// Read here rather than in the provider because two very different things need
// it: the provider, to emit the argument, and the language frontend, to resolve
// the unit's wheels for the same target. Those cannot disagree -- an
// architecture is part of a compiled extension's filename, so a bundle built
// for the wrong one installs cleanly and fails on the first invocation with
// "No module named X" -- and a single reader is how they stay in step.
//
// The spelling is `architectures`, plural and a list, because that is what the
// underlying resource calls it. AWS Lambda takes exactly one.
func (rc ResourceConfig) Architecture() string {
	// Looked for across every provider block rather than in one named
	// resource, because `architectures` is the argument's name wherever it
	// appears -- the spelling comes from the resource schema, not from cloudcc
	// -- and a unit has at most one compute resource to declare it on.
	for _, block := range rc.ProviderArgs {
		list, ok := block["architectures"].([]any)
		if !ok || len(list) == 0 {
			continue
		}
		if name, ok := list[0].(string); ok {
			return name
		}
	}
	return ""
}

// mergeProviderArgs deep-merges per-resource blocks, so a weaker layer can set
// one argument of a resource and a stronger layer another.
func mergeProviderArgs(base, over map[string]map[string]any) map[string]map[string]any {
	if len(base) == 0 && len(over) == 0 {
		return nil
	}
	out := make(map[string]map[string]any, len(base)+len(over))
	for resource, args := range base {
		out[resource] = DeepMerge(nil, args)
	}
	for resource, args := range over {
		out[resource] = DeepMerge(out[resource], args)
	}
	return out
}

// KindDefault holds the provider defaults for one capability kind: the
// kind-wide layer plus per-type overrides.
type KindDefault struct {
	ResourceConfig `yaml:",inline"`
	ByType         map[string]ResourceConfig `yaml:"by_type,omitempty"`
}

// App is the whole of cloudcc.yaml.
type App struct {
	App      string `yaml:"app"`
	Provider string `yaml:"provider"`
	OutDir   string `yaml:"out_dir"`

	Defaults map[string]KindDefault `yaml:"defaults,omitempty"`

	ExecutionUnits map[string]ResourceConfig `yaml:"execution_units,omitempty"`
	Exposed        map[string]ResourceConfig `yaml:"exposed,omitempty"`
	Persisted      map[string]ResourceConfig `yaml:"persisted,omitempty"`
	PubSub         map[string]ResourceConfig `yaml:"pubsub,omitempty"`
	StaticUnits    map[string]ResourceConfig `yaml:"static_units,omitempty"`
	ConfigVars     map[string]ResourceConfig `yaml:"config,omitempty"`

	// Logging is app-wide rather than keyed by id: there is one answer to
	// "where do the logs go" for an application, and a per-unit override would
	// mostly be a way to lose half of them.
	Logging ResourceConfig `yaml:"logging,omitempty"`

	// PulumiParams applies to every generated resource.
	PulumiParams map[string]any `yaml:"pulumi_params,omitempty" json:"pulumi_params,omitempty"`
}

var appNameRe = regexp.MustCompile(`^[\w\-.:/]+$`)

// ValidateAppName enforces the documented app-name rules.
func ValidateAppName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("app name is required (set `app:` in cloudcc.yaml or pass --app)")
	case len(name) > 50:
		return fmt.Errorf("app name %q is %d characters; maximum is 50", name, len(name))
	case !appNameRe.MatchString(name):
		return fmt.Errorf("app name %q is invalid: allowed characters are letters, digits, and _-.:/", name)
	}
	return nil
}

// section returns the config section that holds explicit entries for kind.
func (a *App) section(kind string) map[string]ResourceConfig {
	switch {
	case kind == KindExecutionUnit:
		return a.ExecutionUnits
	case kind == KindExpose:
		return a.Exposed
	case kind == KindPubSub:
		return a.PubSub
	case kind == KindStaticUnit:
		return a.StaticUnits
	case kind == KindConfig:
		return a.ConfigVars
	case IsPersistKind(kind):
		return a.Persisted
	}
	return nil
}

// setSection stores the explicit entry map for kind, creating it if needed.
func (a *App) setSection(kind, id string, rc ResourceConfig) {
	assign := func(m *map[string]ResourceConfig) {
		if *m == nil {
			*m = map[string]ResourceConfig{}
		}
		(*m)[id] = rc
	}
	switch {
	case kind == KindExecutionUnit:
		assign(&a.ExecutionUnits)
	case kind == KindExpose:
		assign(&a.Exposed)
	case kind == KindPubSub:
		assign(&a.PubSub)
	case kind == KindStaticUnit:
		assign(&a.StaticUnits)
	case kind == KindConfig:
		assign(&a.ConfigVars)
	case IsPersistKind(kind):
		assign(&a.Persisted)
	}
}

// AppOutDir is where this application's compiled output goes: a directory
// named after the app, under out_dir.
//
// The nesting is what makes out_dir shareable. `compiled/` holding one app's
// index.ts is fine until the second app is compiled beside it, at which point
// the two silently overwrite each other -- and the first anyone hears of it is
// a deploy that replaces the wrong stack. A folder per app costs one path
// segment and removes the whole class.
func (a *App) AppOutDir() string {
	if a.App == "" {
		return a.OutDir
	}
	return filepath.Join(a.OutDir, a.App)
}

// LogDestination resolves where this application's logs go: the builtin
// default, with anything the file said layered over it.
func (a *App) LogDestination() ResourceConfig {
	out := a.Defaults[KindLogging].ResourceConfig.Merge(a.Logging)
	if out.RetentionDays == 0 {
		out.RetentionDays = DefaultLogRetentionDays
	}
	return out
}

// Lookup resolves the layered configuration for one resource. The returned
// ResourceConfig always has a non-empty Type when the kind has a default.
func (a *App) Lookup(kind, id string) ResourceConfig {
	def := a.Defaults[kind]
	out := def.ResourceConfig

	explicit, hasExplicit := a.section(kind)[id]

	// The type decides which by_type layer applies, so resolve it first.
	typ := out.Type
	if hasExplicit && explicit.Type != "" {
		typ = explicit.Type
	}
	if typ != "" {
		if byType, ok := def.ByType[typ]; ok {
			out = out.Merge(byType)
		}
	}
	if hasExplicit {
		out = out.Merge(explicit)
	}
	out.Type = typ
	return out
}

// HasExplicitType reports whether the user named a type for this resource.
//
// It is what lets a client's own type act as a default without overriding a
// choice the user made deliberately: the library fills a gap, cloudcc.yaml
// settles an argument.
func (a *App) HasExplicitType(kind, id string) bool {
	entry, ok := a.section(kind)[id]
	return ok && entry.Type != ""
}

// Record writes the fully-resolved configuration for a resource back into the
// explicit section, so that compiled/cloudcc.yaml documents every decision (D5).
func (a *App) Record(kind, id string, rc ResourceConfig) {
	a.setSection(kind, id, rc)
}

// AllPulumiParams returns the app-wide pulumi_params deep-merged with the
// resource-specific ones (resource wins).
func (a *App) AllPulumiParams(rc ResourceConfig) map[string]any {
	return DeepMerge(a.PulumiParams, rc.PulumiParams)
}

// ForOutput returns a copy of the configuration as it should be recorded
// alongside the generated project.
//
// out_dir is rewritten to "." because the emitted file sits *in* the output
// directory: recording the absolute path the compile happened to use would
// make otherwise identical output differ between runs, which the golden and
// double-run tests rely on not happening (D18).
func (a *App) ForOutput() *App {
	out := *a
	out.OutDir = "."
	return &out
}

// SortedKeys returns the keys of m in sorted order; used everywhere generation
// iterates a map, to keep output byte-deterministic (D18).
func SortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func mergeStringMap(base, over map[string]string) map[string]string {
	if base == nil && over == nil {
		return nil
	}
	out := make(map[string]string, len(base)+len(over))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}

// DeepMerge recursively merges over into base and returns a new map. Nested
// maps are merged; every other value type is replaced wholesale by over.
func DeepMerge(base, over map[string]any) map[string]any {
	if base == nil && over == nil {
		return nil
	}
	out := make(map[string]any, len(base)+len(over))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		if bm, ok := out[k].(map[string]any); ok {
			if om, ok := v.(map[string]any); ok {
				out[k] = DeepMerge(bm, om)
				continue
			}
		}
		out[k] = v
	}
	return out
}

// UnmarshalYAML splits a declaration into its two layers.
//
// The loader decodes with KnownFields(true), so an unrecognised key is an error
// -- which is what keeps a typo from being silently ignored, and what a
// provider-args block would otherwise trip over: `aws.lambda.Function` is not a
// field of this struct and never will be, because the set of provider resources
// is not the config package's business.
//
// A key containing a dot is a provider resource type. That is not a heuristic
// dressed up as a rule: every field below is a single lower-case word, and
// every provider resource type is dotted -- `aws.lambda.Function`,
// `azure.appservice.FunctionApp`, `gcp.cloudfunctionsv2.Function`. A key with
// no dot is checked against the fields, so `memroy:` still fails and says so.
func (rc *ResourceConfig) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("expected a block of settings, got %s", nodeKind(node))
	}

	// A shadow type with the same fields and no method, so decoding it does not
	// call this function again.
	type plain ResourceConfig
	var known plain
	var provider map[string]map[string]any

	// Two passes over the same node: one for the declared fields with strict
	// checking, one for the dotted keys. Splitting the node first would mean
	// rebuilding it, and a rebuilt node loses the line numbers a decode error
	// would otherwise carry.
	stripped := &yaml.Node{Kind: yaml.MappingNode, Tag: node.Tag, Style: node.Style}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		if !strings.Contains(key.Value, ".") {
			// Checked here rather than left to the decoder. KnownFields is a
			// property of the *decoder*, and node.Decode below builds a fresh
			// one without it -- so writing this method at all switched off the
			// strict field checking the loader asks for, and `memroy: 1024`
			// was quietly accepted. That is precisely the silent acceptance
			// the setting exists to prevent, so the check moves in here.
			if !declaredFields[key.Value] {
				return fmt.Errorf("line %d: no setting called %q. Settings are %s, and a "+
					"key with a dot in it configures a provider resource, as in "+
					"`aws.lambda.Function:`",
					key.Line, key.Value, quotedFields())
			}
			stripped.Content = append(stripped.Content, key, value)
			continue
		}
		if value.Kind != yaml.MappingNode {
			return fmt.Errorf("%s takes a block of arguments for that resource, got %s",
				key.Value, nodeKind(value))
		}
		var args map[string]any
		if err := value.Decode(&args); err != nil {
			return fmt.Errorf("%s: %w", key.Value, err)
		}
		if provider == nil {
			provider = map[string]map[string]any{}
		}
		provider[key.Value] = args
	}

	if err := stripped.Decode(&known); err != nil {
		return err
	}
	*rc = ResourceConfig(known)
	rc.ProviderArgs = provider
	return nil
}

// MarshalYAML writes the two layers back out as one block, so the resolved
// configuration cloudcc emits round-trips through the loader above.
func (rc ResourceConfig) MarshalYAML() (any, error) {
	type plain ResourceConfig
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	if err := enc.Encode(plain(rc)); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}

	var out yaml.Node
	if err := yaml.Unmarshal(buf.Bytes(), &out); err != nil {
		return nil, err
	}
	// An empty declaration encodes as the null document, which has no content
	// to append to.
	if len(out.Content) == 0 || out.Content[0].Kind != yaml.MappingNode {
		if len(rc.ProviderArgs) == 0 {
			return plain(rc), nil
		}
		out = yaml.Node{Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
	}
	mapping := out.Content[0]

	// Sorted, because the generated file is compared byte for byte (D18) and
	// Go map iteration is not.
	resources := make([]string, 0, len(rc.ProviderArgs))
	for name := range rc.ProviderArgs {
		resources = append(resources, name)
	}
	sort.Strings(resources)
	for _, name := range resources {
		var value yaml.Node
		if err := value.Encode(rc.ProviderArgs[name]); err != nil {
			return nil, err
		}
		mapping.Content = append(mapping.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: name}, &value)
	}
	return mapping, nil
}

func nodeKind(n *yaml.Node) string {
	switch n.Kind {
	case yaml.ScalarNode:
		return "a single value"
	case yaml.SequenceNode:
		return "a list"
	case yaml.MappingNode:
		return "a block"
	}
	return "something else"
}

// declaredFields is the set of yaml keys ResourceConfig declares, derived from
// the struct so it cannot drift from it.
var declaredFields = func() map[string]bool {
	out := map[string]bool{}
	t := reflect.TypeOf(ResourceConfig{})
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("yaml")
		if name, _, _ := strings.Cut(tag, ","); name != "" && name != "-" {
			out[name] = true
		}
	}
	return out
}()

// quotedFields lists the declared settings for a diagnostic.
func quotedFields() string {
	names := make([]string, 0, len(declaredFields))
	for name := range declaredFields {
		names = append(names, name)
	}
	sort.Strings(names)
	for i, n := range names {
		names[i] = "`" + n + "`"
	}
	return strings.Join(names, ", ")
}
