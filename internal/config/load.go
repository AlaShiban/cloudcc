package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// DefaultFileName is the conventional name of the configuration file.
const DefaultFileName = "cloudcc.yaml"

// DefaultOutDir is used when neither cloudcc.yaml nor -o specifies one.
const DefaultOutDir = "compiled"

// ProviderAWS is the only provider implemented in v1 (D9).
const ProviderAWS = "aws"

// BuiltinDefaults returns the provider defaults every App starts from. Callers
// get a fresh copy each time; the returned value is safe to mutate.
func BuiltinDefaults() map[string]KindDefault {
	kd := func(typ string) KindDefault {
		return KindDefault{ResourceConfig: ResourceConfig{Type: typ}}
	}
	return map[string]KindDefault{
		KindExecutionUnit: kd("lambda"),
		KindExpose:        kd("apigateway"),
		KindPersistKV:     kd("dynamodb"),
		KindPersistFS:     kd("s3"),
		KindPersistSecret: kd("secretsmanager"),
		KindPersistORM:    kd("rds_postgres"),
		KindPersistRedis:  kd("elasticache"),
		KindPubSub:        kd("sns"),
		KindStaticUnit:    kd("s3"),
		KindConfig:        {},
	}
}

// New returns an App populated with builtin defaults.
func New() *App {
	return &App{
		Provider: ProviderAWS,
		OutDir:   DefaultOutDir,
		Defaults: BuiltinDefaults(),
	}
}

// Load reads cloudcc.yaml from path. A missing file is not an error: the caller
// gets an App with builtin defaults, so `cloudcc compile ./app --app foo` works with
// no configuration file at all.
func Load(path string) (*App, error) {
	app := New()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return app, nil
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	// Decode into a bare App so that absent keys leave the builtin defaults
	// alone, then layer the file's defaults over the builtins.
	var file App
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&file); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	app.mergeFile(&file)
	return app, nil
}

// FindFile locates the configuration file: the explicit path if given,
// otherwise cloudcc.yaml in srcRoot, otherwise cloudcc.yaml in the working directory.
func FindFile(explicit, srcRoot string) string {
	if explicit != "" {
		return explicit
	}
	inSrc := filepath.Join(srcRoot, DefaultFileName)
	if _, err := os.Stat(inSrc); err == nil {
		return inSrc
	}
	return DefaultFileName
}

func (a *App) mergeFile(f *App) {
	if f.App != "" {
		a.App = f.App
	}
	if f.Provider != "" {
		a.Provider = f.Provider
	}
	if f.OutDir != "" {
		a.OutDir = f.OutDir
	}
	for kind, kd := range f.Defaults {
		base := a.Defaults[kind]
		merged := KindDefault{ResourceConfig: base.ResourceConfig.Merge(kd.ResourceConfig)}
		merged.ByType = map[string]ResourceConfig{}
		for t, rc := range base.ByType {
			merged.ByType[t] = rc
		}
		for t, rc := range kd.ByType {
			merged.ByType[t] = merged.ByType[t].Merge(rc)
		}
		if len(merged.ByType) == 0 {
			merged.ByType = nil
		}
		a.Defaults[kind] = merged
	}
	a.ExecutionUnits = mergeSection(a.ExecutionUnits, f.ExecutionUnits)
	a.Exposed = mergeSection(a.Exposed, f.Exposed)
	a.Persisted = mergeSection(a.Persisted, f.Persisted)
	a.PubSub = mergeSection(a.PubSub, f.PubSub)
	a.StaticUnits = mergeSection(a.StaticUnits, f.StaticUnits)
	a.ConfigVars = mergeSection(a.ConfigVars, f.ConfigVars)
	a.PulumiParams = DeepMerge(a.PulumiParams, f.PulumiParams)
}

func mergeSection(base, over map[string]ResourceConfig) map[string]ResourceConfig {
	if base == nil && over == nil {
		return nil
	}
	out := map[string]ResourceConfig{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		out[k] = out[k].Merge(v)
	}
	return out
}

// Validate checks the invariants that do not depend on the source program.
func (a *App) Validate() error {
	if err := ValidateAppName(a.App); err != nil {
		return err
	}
	if a.Provider != ProviderAWS {
		return fmt.Errorf("unsupported provider %q: only %q is implemented", a.Provider, ProviderAWS)
	}
	if a.OutDir == "" {
		return fmt.Errorf("out_dir must not be empty")
	}
	for _, kind := range SortedKeys(a.Defaults) {
		if !IsKind(kind) {
			return fmt.Errorf("defaults: unknown capability kind %q", kind)
		}
	}
	return nil
}

// Marshal renders the app configuration as YAML. Map keys are emitted in
// sorted order by yaml.v3, which keeps the output byte-deterministic (D18).
func (a *App) Marshal() ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(a); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
