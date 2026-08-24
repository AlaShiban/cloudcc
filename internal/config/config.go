// Package config defines cc.yaml: the single, layered configuration file that
// drives every type-selection decision the compiler makes (D5).
//
// Layering, from weakest to strongest:
//
//	defaults.<kind>                 -- provider default-by-kind
//	defaults.<kind>.by_type.<type>  -- provider default-by-type
//	<section>.<id>                  -- explicit per-resource
//
// The fully-resolved result is written back out to compiled/cc.yaml so that
// every inferred decision is inspectable.
package config

import (
	"fmt"
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
	StaticFiles   string `yaml:"static_files,omitempty" json:"static_files,omitempty"`
	SharedFiles   string `yaml:"shared_files,omitempty" json:"shared_files,omitempty"`
	IndexDocument string `yaml:"index_document,omitempty" json:"index_document,omitempty"`
	// PulumiParams is the escape hatch: deep-merged into the generated Pulumi
	// resource args for this resource.
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
	out.EnvironmentVariables = mergeStringMap(rc.EnvironmentVariables, other.EnvironmentVariables)
	out.PulumiParams = DeepMerge(rc.PulumiParams, other.PulumiParams)
	return out
}

// KindDefault holds the provider defaults for one capability kind: the
// kind-wide layer plus per-type overrides.
type KindDefault struct {
	ResourceConfig `yaml:",inline"`
	ByType         map[string]ResourceConfig `yaml:"by_type,omitempty"`
}

// App is the whole of cc.yaml.
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

	// PulumiParams applies to every generated resource.
	PulumiParams map[string]any `yaml:"pulumi_params,omitempty" json:"pulumi_params,omitempty"`
}

var appNameRe = regexp.MustCompile(`^[\w\-.:/]+$`)

// ValidateAppName enforces the documented app-name rules.
func ValidateAppName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("app name is required (set `app:` in cc.yaml or pass --app)")
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

// Record writes the fully-resolved configuration for a resource back into the
// explicit section, so that compiled/cc.yaml documents every decision (D5).
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
