package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestBuiltinDefaultsCoverEveryKind(t *testing.T) {
	d := BuiltinDefaults()
	for _, k := range Kinds {
		if _, ok := d[k]; !ok {
			t.Errorf("kind %q has no builtin default entry", k)
		}
	}
	if len(d) != len(Kinds) {
		t.Errorf("builtin defaults has %d entries, Kinds has %d", len(d), len(Kinds))
	}
}

func TestLookupLayering(t *testing.T) {
	a := New()
	a.App = "petstore"
	a.Defaults[KindPersistKV] = KindDefault{
		ResourceConfig: ResourceConfig{
			Type:         "dynamodb",
			PulumiParams: map[string]any{"kindWide": true, "shared": "from-kind"},
		},
		ByType: map[string]ResourceConfig{
			"dynamodb": {PulumiParams: map[string]any{"byType": true, "shared": "from-type"}},
		},
	}
	a.Persisted = map[string]ResourceConfig{
		"petsByOwner": {PulumiParams: map[string]any{"explicit": true, "shared": "from-explicit"}},
	}

	got := a.Lookup(KindPersistKV, "petsByOwner")
	if got.Type != "dynamodb" {
		t.Fatalf("type = %q, want dynamodb", got.Type)
	}
	for k, want := range map[string]any{
		"kindWide": true, "byType": true, "explicit": true, "shared": "from-explicit",
	} {
		if got.PulumiParams[k] != want {
			t.Errorf("pulumi_params[%q] = %v, want %v", k, got.PulumiParams[k], want)
		}
	}
}

func TestLookupExplicitTypeSelectsByTypeLayer(t *testing.T) {
	a := New()
	a.Defaults[KindPersistRedis] = KindDefault{
		ResourceConfig: ResourceConfig{Type: "elasticache"},
		ByType: map[string]ResourceConfig{
			"elasticache": {PulumiParams: map[string]any{"node": true}},
			"memorydb":    {PulumiParams: map[string]any{"cluster": true}},
		},
	}
	a.Persisted = map[string]ResourceConfig{"cache": {Type: "memorydb"}}

	got := a.Lookup(KindPersistRedis, "cache")
	if got.Type != "memorydb" {
		t.Fatalf("type = %q, want memorydb", got.Type)
	}
	if got.PulumiParams["cluster"] != true {
		t.Errorf("memorydb by_type layer not applied: %v", got.PulumiParams)
	}
	if _, ok := got.PulumiParams["node"]; ok {
		t.Errorf("elasticache by_type layer leaked in: %v", got.PulumiParams)
	}
}

func TestLookupUnconfiguredIDStillGetsDefaultType(t *testing.T) {
	a := New()
	if got := a.Lookup(KindPersistKV, "never-mentioned"); got.Type != "dynamodb" {
		t.Errorf("type = %q, want dynamodb", got.Type)
	}
}

func TestPersistKindsShareTheSameSection(t *testing.T) {
	a := New()
	a.Persisted = map[string]ResourceConfig{"blobs": {Type: "s3"}}
	if got := a.Lookup(KindPersistFS, "blobs"); got.Type != "s3" {
		t.Errorf("persist_fs lookup did not read the persisted section: %+v", got)
	}
}

func TestDeepMerge(t *testing.T) {
	base := map[string]any{
		"a":      1,
		"nested": map[string]any{"keep": "yes", "override": "old"},
	}
	over := map[string]any{
		"b":      2,
		"nested": map[string]any{"override": "new"},
	}
	got := DeepMerge(base, over)
	n := got["nested"].(map[string]any)
	if n["keep"] != "yes" || n["override"] != "new" || got["a"] != 1 || got["b"] != 2 {
		t.Errorf("DeepMerge = %#v", got)
	}
	if base["nested"].(map[string]any)["override"] != "old" {
		t.Error("DeepMerge mutated its base argument")
	}
}

func TestValidateAppName(t *testing.T) {
	cases := []struct {
		name string
		ok   bool
	}{
		{"petstore", true},
		{"pet-store.v2:1/a", true},
		{"", false},
		{"has space", false},
		{"bang!", false},
		{strings.Repeat("a", 51), false},
	}
	for _, c := range cases {
		err := ValidateAppName(c.name)
		if (err == nil) != c.ok {
			t.Errorf("ValidateAppName(%q) err = %v, want ok=%v", c.name, err, c.ok)
		}
	}
}

func TestValidateRejectsUnknownProvider(t *testing.T) {
	a := New()
	a.App = "x"
	a.Provider = "gcp"
	if err := a.Validate(); err == nil || !strings.Contains(err.Error(), "gcp") {
		t.Errorf("expected provider error, got %v", err)
	}
}

func TestLoadMissingFileYieldsDefaults(t *testing.T) {
	a, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Defaults[KindExecutionUnit].Type != TypeFunction {
		t.Errorf("missing file should still give builtin defaults, got %+v", a.Defaults)
	}
}

func TestLoadLayersOverBuiltins(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cloudcc.yaml")
	must(t, os.WriteFile(path, []byte(`
app: petstore
defaults:
  execution_unit:
    type: container
persisted:
  petsByOwner:
    type: dynamodb
`), 0o644))

	a, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if a.App != "petstore" {
		t.Errorf("app = %q", a.App)
	}
	if a.Defaults[KindExecutionUnit].Type != TypeContainer {
		t.Errorf("execution_unit default not overridden: %+v", a.Defaults[KindExecutionUnit])
	}
	// Untouched kinds keep their builtin values.
	if a.Defaults[KindPersistKV].Type != "dynamodb" {
		t.Errorf("persist_kv default lost: %+v", a.Defaults[KindPersistKV])
	}
	if a.Provider != ProviderAWS || a.OutDir != DefaultOutDir {
		t.Errorf("provider/out_dir defaults lost: %q %q", a.Provider, a.OutDir)
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cloudcc.yaml")
	must(t, os.WriteFile(path, []byte("app: x\nnot_a_field: 1\n"), 0o644))
	if _, err := Load(path); err == nil {
		t.Fatal("expected an error for an unknown field")
	}
}

// Generated output must be byte-identical across runs (D18); yaml.v3 sorting
// map keys is load-bearing for that, so assert it directly.
func TestMarshalIsDeterministic(t *testing.T) {
	build := func() *App {
		a := New()
		a.App = "petstore"
		a.Persisted = map[string]ResourceConfig{}
		for _, id := range []string{"zeta", "alpha", "mid", "beta", "omega", "gamma", "delta"} {
			a.Persisted[id] = ResourceConfig{Type: "dynamodb", PulumiParams: map[string]any{
				"z": 1, "a": 2, "m": 3, "b": 4,
			}}
		}
		return a
	}
	first, err := build().Marshal()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		next, err := build().Marshal()
		if err != nil {
			t.Fatal(err)
		}
		if string(next) != string(first) {
			t.Fatalf("marshal output differs on run %d:\n--- first ---\n%s\n--- next ---\n%s", i, first, next)
		}
	}
	if i, j := strings.Index(string(first), "alpha"), strings.Index(string(first), "beta"); i > j {
		t.Errorf("map keys are not sorted; output:\n%s", first)
	}
}

func TestRoundTrip(t *testing.T) {
	a := New()
	a.App = "petstore"
	a.Record(KindPersistKV, "petsByOwner", ResourceConfig{
		Type:         "dynamodb",
		PulumiParams: map[string]any{"billingMode": "PAY_PER_REQUEST"},
	})
	a.Record(KindExecutionUnit, "api", ResourceConfig{
		Type:                 TypeFunction,
		EnvironmentVariables: map[string]string{"LOG_LEVEL": "info"},
	})

	data, err := a.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "cloudcc.yaml")
	must(t, os.WriteFile(path, data, 0o644))

	back, err := Load(path)
	if err != nil {
		t.Fatalf("re-loading emitted config failed: %v\n%s", err, data)
	}
	if got := back.Lookup(KindPersistKV, "petsByOwner"); got.Type != "dynamodb" ||
		got.PulumiParams["billingMode"] != "PAY_PER_REQUEST" {
		t.Errorf("round-trip lost persist config: %+v", got)
	}
	if got := back.Lookup(KindExecutionUnit, "api"); got.EnvironmentVariables["LOG_LEVEL"] != "info" {
		t.Errorf("round-trip lost env vars: %+v", got)
	}

	again, err := back.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(data) {
		t.Errorf("round-trip is not stable:\n--- first ---\n%s\n--- second ---\n%s", data, again)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// A misspelled setting is an error, and writing UnmarshalYAML must not switch
// that off.
//
// KnownFields is a property of the *decoder*, and a custom UnmarshalYAML
// decodes through node.Decode, which builds a fresh decoder without it. So
// adding the method to collect `aws.lambda.Function:` blocks silently disabled
// strict field checking for every declaration in the file, and `memroy: 1024`
// compiled cleanly and did nothing. The check lives inside the method now.
func TestAMisspelledSettingIsStillAnError(t *testing.T) {
	var rc ResourceConfig
	err := yaml.Unmarshal([]byte("type: function\nmemroy: 1024\n"), &rc)
	if err == nil {
		t.Fatalf("memroy was accepted: %+v", rc)
	}
	for _, want := range []string{`"memroy"`, "`memory`", "aws.lambda.Function"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the diagnostic is missing %q:\n%s", want, err)
		}
	}
}

// The two layers survive a round trip, because the resolved configuration is
// written back out and has to load again.
func TestBothLayersRoundTripThroughYAML(t *testing.T) {
	const src = `type: function
memory: 1024
timeout: 60
aws.lambda.Function:
    architectures:
        - arm64
    ephemeral_storage:
        size: 2048
`
	var rc ResourceConfig
	if err := yaml.Unmarshal([]byte(src), &rc); err != nil {
		t.Fatal(err)
	}
	if rc.Type != TypeFunction || rc.Memory != 1024 || rc.Timeout != 60 {
		t.Errorf("portable layer = %+v", rc)
	}
	block, ok := rc.ProviderArgs["aws.lambda.Function"]
	if !ok {
		t.Fatalf("provider block missing: %+v", rc.ProviderArgs)
	}
	if _, ok := block["ephemeral_storage"]; !ok {
		t.Errorf("provider block = %+v", block)
	}

	out, err := yaml.Marshal(rc)
	if err != nil {
		t.Fatal(err)
	}
	var again ResourceConfig
	if err := yaml.Unmarshal(out, &again); err != nil {
		t.Fatalf("the emitted configuration does not load again: %v\n%s", err, out)
	}
	if again.Memory != rc.Memory || again.Timeout != rc.Timeout {
		t.Errorf("portable layer changed on the way round: %+v -> %+v", rc, again)
	}
	if len(again.ProviderArgs) != len(rc.ProviderArgs) {
		t.Errorf("provider blocks changed on the way round: %+v -> %+v\n%s",
			rc.ProviderArgs, again.ProviderArgs, out)
	}
}

// A compute type named after one cloud's product is an error naming the
// portable one, rather than a silent alias.
func TestAProductNameForAComputeTypeIsRenamed(t *testing.T) {
	for old, now := range map[string]string{"lambda": TypeFunction, "ecs": TypeContainer} {
		err := CheckComputeType("api", old)
		if err == nil {
			t.Fatalf("`type: %s` was accepted", old)
		}
		if !strings.Contains(err.Error(), now) {
			t.Errorf("the error does not name the replacement %q: %v", now, err)
		}
	}
	if err := CheckComputeType("api", TypeFunction); err != nil {
		t.Errorf("`type: function` was rejected: %v", err)
	}
}
