package capabilities

import (
	"reflect"
	"strings"
	"testing"

	"github.com/cloudcompiler/cc/internal/compiler"
)

// TestChainSchedule pins the order the scheduler derives.
//
// Plugins declare dependencies and the order is computed, so this is not a
// specification -- it is a record. It exists because two of these orderings
// are load-bearing in ways nothing else would catch: static-units and
// embed-assets must claim files before exec-units assembles closures, and
// resolve:aws must run after shims, which is what decides how each unit is
// packaged.
func TestChainSchedule(t *testing.T) {
	c, err := compiler.NewCompiler(Chain())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		PluginConfig,
		PluginInput,
		PluginDetect,
		PluginEmbedAssets,
		PluginStaticUnits,
		PluginExecUnits,
		PluginConfigVars,
		PluginExpose,
		PluginPersist,
		PluginPubSub,
		PluginValidate,
		PluginShims,
		PluginResolveAWS,
		PluginRender,
		PluginTopology,
	}
	if got := c.Order(); !reflect.DeepEqual(got, want) {
		t.Errorf("schedule =\n  %s\nwant\n  %s",
			strings.Join(got, " -> "), strings.Join(want, " -> "))
	}
}

// TestClaimingHappensBeforeClosure is the ordering that actually matters: if
// it inverts, static assets get swallowed into every compute bundle.
func TestClaimingHappensBeforeClosure(t *testing.T) {
	c, err := compiler.NewCompiler(Chain())
	if err != nil {
		t.Fatal(err)
	}
	order := c.Order()
	at := func(name string) int {
		for i, n := range order {
			if n == name {
				return i
			}
		}
		t.Fatalf("%s is not in the schedule: %v", name, order)
		return -1
	}
	for _, claimer := range []string{PluginStaticUnits, PluginEmbedAssets} {
		if at(claimer) >= at(PluginExecUnits) {
			t.Errorf("%s must run before %s, or claimed files end up in compute bundles",
				claimer, PluginExecUnits)
		}
	}
	if at(PluginShims) >= at(PluginResolveAWS) {
		t.Errorf("%s must run before %s: the resolver needs to know how each unit is packaged",
			PluginShims, PluginResolveAWS)
	}
	if at(PluginValidate) >= at(PluginShims) {
		t.Errorf("%s must run before anything is generated", PluginValidate)
	}
}

// TestIntentChainCreatesNoResources pins the two-layer separation (D7).
func TestIntentChainCreatesNoResources(t *testing.T) {
	for _, p := range IntentChain() {
		switch p.Name() {
		case PluginResolveAWS, PluginRender, PluginTopology, PluginShims:
			t.Errorf("%s is a generating stage and does not belong in the intent chain", p.Name())
		}
	}
}

func TestEveryPluginNameIsUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range Chain() {
		if seen[p.Name()] {
			t.Errorf("duplicate plugin name %q", p.Name())
		}
		seen[p.Name()] = true
	}
}
