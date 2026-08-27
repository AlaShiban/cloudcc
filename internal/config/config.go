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
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
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
	Resources map[string]any `yaml:"resources,omitempty" json:"resources,omitempty"`
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
	out.Resources = DeepMerge(rc.Resources, other.Resources)
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
	list, ok := rc.Resources["architectures"].([]any)
	if !ok || len(list) == 0 {
		return ""
	}
	name, _ := list[0].(string)
	return name
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
