package loadtest

import (
	"strings"
	"testing"
)

func reportPair() (*Report, *Report) {
	plan := &Plan{App: "shop", Unit: "api"}
	before := &Report{App: "shop", Mode: "uncompiled", Unit: "api", Plan: plan, Runs: []Summary{
		{Strategy: "steady", Throughput: 500, P50: "2ms", P99: "5ms", OKRate: 1},
		{Strategy: "ramp", Throughput: 800, P50: "3ms", P99: "9ms", OKRate: 1},
	}}
	after := &Report{App: "shop", Mode: "compiled", Unit: "api", Plan: plan, Runs: []Summary{
		{Strategy: "steady", Throughput: 250, P50: "4ms", P99: "11ms", OKRate: 1},
		{Strategy: "ramp", Throughput: 800, P50: "3ms", P99: "9ms", OKRate: 0.5},
	}}
	return before, after
}

func TestCompareReportsARatioPerStrategy(t *testing.T) {
	before, after := reportPair()
	deltas, missing := Compare(before, after)

	if len(missing) != 0 {
		t.Errorf("nothing should be missing: %v", missing)
	}
	if len(deltas) != 2 {
		t.Fatalf("deltas = %d", len(deltas))
	}
	byName := map[string]Delta{}
	for _, d := range deltas {
		byName[d.Strategy] = d
	}
	// Halved throughput is a 0.5x ratio, and the ratio is what a reader
	// compares between runs -- an absolute number means nothing without the
	// machine it was measured on.
	if got := byName["steady"].Ratio; got != 0.5 {
		t.Errorf("steady ratio = %v, want 0.5", got)
	}
	if got := byName["ramp"].Ratio; got != 1 {
		t.Errorf("ramp ratio = %v, want 1", got)
	}
}

// A strategy that ran on one side only is a note, not a delta. The alternative
// is a table row implying a regression from nothing, which is worse than
// saying plainly that the two runs are not comparable.
func TestAStrategyOnOneSideOnlyIsReportedRatherThanCompared(t *testing.T) {
	before, after := reportPair()
	after.Runs = append(after.Runs, Summary{Strategy: "burst", Throughput: 100})
	before.Runs = append(before.Runs, Summary{Strategy: "drain", Throughput: 100})

	deltas, missing := Compare(before, after)
	if len(deltas) != 2 {
		t.Errorf("only the shared strategies should be compared, got %d", len(deltas))
	}
	joined := strings.Join(missing, "; ")
	if !strings.Contains(joined, "burst ran only after") || !strings.Contains(joined, "drain ran only before") {
		t.Errorf("missing = %v", missing)
	}
}

// The point of the whole exercise: an edge that carried nothing has to be
// reported as dead, and has to explain what that means.
func TestADeadEdgeIsReportedAndExplained(t *testing.T) {
	report := &Report{App: "shop", Connected: []Observation{
		{Edge: "execution_unit/api -uses-> persist_kv/pets", Kind: "store", Observed: 0,
			State: StateDead, OK: false,
			Why: "unit \"api\" declares store \"pets\" and the compiler wired it, but nothing reached it under load"},
		{Edge: "expose/x -exposes-> execution_unit/api", Kind: "http", Observed: 900,
			State: StateCarried, OK: true},
		{Edge: "execution_unit/pricing -uses-> persist_kv/menuPrices", Kind: "store", Observed: 0,
			State: StateUnverified, OK: true,
			Why: "holds no rows, and the unit did run; a read-only path and a dead write path look alike here"},
	}}

	dead := report.DeadEdges()
	if len(dead) != 1 || dead[0].Kind != "store" {
		t.Fatalf("dead edges = %+v", dead)
	}

	rendered := RenderConnectedness(report)
	if !strings.Contains(rendered, "DEAD") {
		t.Errorf("a dead edge is not marked:\n%s", rendered)
	}
	if !strings.Contains(rendered, "nothing reached it under load") {
		t.Errorf("a dead edge does not explain itself:\n%s", rendered)
	}
	// A healthy edge is shown too: "the ones that worked" is how a reader
	// tells "one edge is dead" from "the checker did not run".
	if !strings.Contains(rendered, "http=900") {
		t.Errorf("healthy edges are not shown:\n%s", rendered)
	}

	// An edge this harness cannot settle is neither a pass nor a defect, and
	// the difference matters: a store a unit only reads leaves no trace in an
	// emulator that serves no read counters. Reporting it as dead would be a
	// false accusation, and reporting it as fine would be a check that passes
	// without looking.
	unverified := report.UnverifiedEdges()
	if len(unverified) != 1 || !strings.Contains(unverified[0].Edge, "menuPrices") {
		t.Errorf("unverified = %+v", unverified)
	}
	if strings.Count(rendered, "DEAD") != 1 {
		t.Errorf("only the dead edge should be marked DEAD:\n%s", rendered)
	}
	if !strings.Contains(rendered, "  ? ") {
		t.Errorf("an unverified edge is not distinguished:\n%s", rendered)
	}
}

func TestAReportWithNoDeadEdgesHasNone(t *testing.T) {
	report := &Report{Connected: []Observation{
		{Edge: "a", OK: true, Observed: 1, State: StateCarried},
		{Edge: "b", OK: true, Observed: 42, State: StateCarried},
	}}
	if dead := report.DeadEdges(); len(dead) != 0 {
		t.Errorf("dead = %+v", dead)
	}
}

func TestTheComparisonTableNamesBothSides(t *testing.T) {
	before, after := reportPair()
	table := RenderComparison(before, after)

	for _, want := range []string{"uncompiled", "compiled", "steady", "ramp", "0.50x"} {
		if !strings.Contains(table, want) {
			t.Errorf("the table does not mention %q:\n%s", want, table)
		}
	}
}

// The comparison is a ratio and not a verdict, and there is a reason for that:
// a run whose throughput went *up* while an edge died is a change worth
// noticing, not a win. Removing a store write makes an application faster and
// wrong, which is exactly what a benchmark on its own would celebrate.
func TestFasterIsNotTheSameAsBetter(t *testing.T) {
	plan := &Plan{App: "shop", Unit: "api"}
	before := &Report{App: "shop", Mode: "uncompiled", Plan: plan, Runs: []Summary{
		{Strategy: "steady", Throughput: 500, OKRate: 1},
	}}
	after := &Report{App: "shop", Mode: "compiled", Plan: plan, Runs: []Summary{
		{Strategy: "steady", Throughput: 900, OKRate: 1},
	}, Connected: []Observation{
		{Edge: "execution_unit/api -uses-> persist_kv/pets", Kind: "store",
			State: StateDead, Why: "nothing reached it"},
	}}

	deltas, _ := Compare(before, after)
	if deltas[0].Ratio <= 1 {
		t.Fatalf("this run got faster: ratio %v", deltas[0].Ratio)
	}
	// And is still a failure, because the throughput came from not doing the
	// work. The two results are reported separately for exactly this case.
	if len(after.DeadEdges()) != 1 {
		t.Error("a faster run with a dead edge must still report the dead edge")
	}
}
