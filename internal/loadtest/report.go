package loadtest

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Report is everything one side of a comparison produced.
type Report struct {
	App string `json:"app"`
	// Mode is "uncompiled" or "compiled" -- the before and after of the
	// comparison this repository cares about.
	Mode string `json:"mode"`
	Unit string `json:"unit"`
	// Runs is one summary per strategy.
	Runs []Summary `json:"runs"`
	// Plan is kept so a report can be read on its own, without the IR it came
	// from and without the version of the application that produced it.
	Plan *Plan `json:"plan"`
	// Connected is the checklist result, filled in by whatever can see the
	// deployed resources. Empty for an uncompiled run, where the edges are
	// in-process and there is nothing external to look at.
	Connected []Observation `json:"connected,omitempty"`
}

// Observation states. Three, not two, because "no evidence" and "no traffic"
// are different claims and only one of them is a defect.
const (
	// StateCarried means the edge demonstrably carried something.
	StateCarried = "carried"
	// StateDead means it did not, and the unit at its source did run -- so
	// there was an opportunity and nothing took it.
	StateDead = "dead"
	// StateUnverified means the evidence this harness can gather does not
	// settle the question. A store read leaves no trace in an emulator that
	// serves no read counters; saying so beats guessing either way.
	StateUnverified = "unverified"
)

// Observation is one expectation and what was found.
type Observation struct {
	Edge string `json:"edge"`
	Kind string `json:"kind"`
	// Observed is the count found: rows written, objects stored, invocations.
	Observed int `json:"observed"`
	// State is one of the three above.
	State string `json:"state"`
	// OK is whether the edge is not a defect: carried or unverified.
	OK bool `json:"ok"`
	// Why is the expectation's explanation, carried through so a failing
	// report explains itself without the plan beside it.
	Why string `json:"why,omitempty"`
}

// Delta is one strategy's before-and-after.
type Delta struct {
	Strategy string `json:"strategy"`

	ThroughputBefore float64 `json:"throughput_before"`
	ThroughputAfter  float64 `json:"throughput_after"`
	// Ratio is after ÷ before. Below 1 means compiling cost throughput.
	Ratio float64 `json:"throughput_ratio"`

	P50Before string `json:"p50_before"`
	P50After  string `json:"p50_after"`
	P99Before string `json:"p99_before"`
	P99After  string `json:"p99_after"`

	OKBefore float64 `json:"ok_rate_before"`
	OKAfter  float64 `json:"ok_rate_after"`

	// LowSample is set when either side recorded too few requests for the
	// ratio to carry weight.
	LowSample bool `json:"low_sample,omitempty"`
}

func anyLowSample(deltas []Delta) bool {
	for _, d := range deltas {
		if d.LowSample {
			return true
		}
	}
	return false
}

// Compare pairs the two reports' runs by strategy.
//
// Only strategies both sides ran are compared. A strategy present on one side
// only is reported as missing rather than as a change, because the alternative
// is a table row implying a regression from nothing.
func Compare(before, after *Report) ([]Delta, []string) {
	byName := map[string]Summary{}
	for _, run := range before.Runs {
		byName[run.Strategy] = run
	}

	var deltas []Delta
	var missing []string
	for _, run := range after.Runs {
		prior, ok := byName[run.Strategy]
		if !ok {
			missing = append(missing, run.Strategy+" ran only after")
			continue
		}
		ratio := 0.0
		if prior.Throughput > 0 {
			ratio = run.Throughput / prior.Throughput
		}
		deltas = append(deltas, Delta{
			Strategy:         run.Strategy,
			ThroughputBefore: prior.Throughput,
			ThroughputAfter:  run.Throughput,
			Ratio:            ratio,
			P50Before:        prior.P50,
			P50After:         run.P50,
			P99Before:        prior.P99,
			P99After:         run.P99,
			OKBefore:         prior.OKRate,
			OKAfter:          run.OKRate,
			LowSample:        prior.LowSample || run.LowSample,
		})
		delete(byName, run.Strategy)
	}
	for name := range byName {
		missing = append(missing, name+" ran only before")
	}
	sort.Strings(missing)
	sort.Slice(deltas, func(i, j int) bool { return deltas[i].Strategy < deltas[j].Strategy })
	return deltas, missing
}

// RenderComparison writes the benchmark table.
//
// Throughput is reported as a ratio rather than a verdict. Compiling moves an
// application from one process to several behind a gateway, and a ratio below
// one is the expected cost of that rather than a regression -- what the number
// is for is noticing when it changes, which needs a baseline rather than a
// threshold invented here.
func RenderComparison(before, after *Report) string {
	deltas, missing := Compare(before, after)

	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s vs %s\n", after.App, before.Mode, after.Mode)
	fmt.Fprintf(&b, "%s\n", strings.Repeat("-", 78))
	fmt.Fprintf(&b, "%-9s %12s %12s %8s %10s %10s %7s\n",
		"strategy", "req/s "+short(before.Mode), "req/s "+short(after.Mode),
		"ratio", "p99 "+short(before.Mode), "p99 "+short(after.Mode), "ok")
	for _, d := range deltas {
		mark := ""
		if d.LowSample {
			mark = "  ?"
		}
		fmt.Fprintf(&b, "%-9s %12.1f %12.1f %7.2fx %10s %10s %6.1f%%%s\n",
			d.Strategy, d.ThroughputBefore, d.ThroughputAfter, d.Ratio,
			d.P99Before, d.P99After, d.OKAfter*100, mark)
	}
	if anyLowSample(deltas) {
		fmt.Fprintf(&b, "  ? too few requests on one side for that ratio to mean much\n")
	}
	for _, m := range missing {
		fmt.Fprintf(&b, "  note: %s\n", m)
	}
	return b.String()
}

func short(mode string) string {
	if len(mode) <= 4 {
		return mode
	}
	return mode[:4]
}

// RenderRuns writes one report's runs as a table.
func RenderRuns(r *Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s), unit %q\n", r.App, r.Mode, r.Unit)
	for _, run := range r.Runs {
		note := ""
		if run.LowSample {
			note = "   (too few requests to mean much)"
		}
		fmt.Fprintf(&b, "  %-7s %6d req in %-8s %8.1f req/s   p50 %-9s p99 %-9s  ok %5.1f%%  routes %d/%d%s\n",
			run.Strategy, run.Requests, run.Duration, run.Throughput,
			run.P50, run.P99, run.OKRate*100, run.RoutesHit, run.RoutesTotal, note)
		// Statuses at or above 400, and transport failures, both itemised.
		// An ok rate of 25% with only a reset count printed sends the reader
		// looking for a network problem when three quarters of the requests
		// came back as a clean 404 -- which is a different bug entirely, and
		// is what actually happened here the first time.
		for _, code := range sortedKeys(run.ByStatus) {
			if code >= "400" {
				fmt.Fprintf(&b, "            HTTP %s: %d\n", code, run.ByStatus[code])
			}
		}
		for _, kind := range sortedKeys(run.Failures) {
			fmt.Fprintf(&b, "            %s: %d\n", kind, run.Failures[kind])
		}
	}
	return b.String()
}

// RenderConnectedness writes the checklist result.
func RenderConnectedness(r *Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "connectedness: %d edge(s)\n", len(r.Connected))
	for _, o := range r.Connected {
		mark := "ok  "
		switch o.State {
		case StateDead:
			mark = "DEAD"
		case StateUnverified:
			mark = "  ? "
		}
		fmt.Fprintf(&b, "  %s %-58s %s=%d\n", mark, o.Edge, o.Kind, o.Observed)
		if o.State != StateCarried && o.Why != "" {
			fmt.Fprintf(&b, "       %s\n", o.Why)
		}
	}
	return b.String()
}

// DeadEdges returns the edges that had an opportunity to carry something and
// did not. An unverified edge is not one of them: it is a limit of what can be
// observed from outside, and reporting it as a defect would train a reader to
// ignore the list.
func (r *Report) DeadEdges() []Observation {
	var out []Observation
	for _, o := range r.Connected {
		if o.State == StateDead {
			out = append(out, o)
		}
	}
	return out
}

// UnverifiedEdges returns the edges this harness could not settle either way.
func (r *Report) UnverifiedEdges() []Observation {
	var out []Observation
	for _, o := range r.Connected {
		if o.State == StateUnverified {
			out = append(out, o)
		}
	}
	return out
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TotalDuration is how long every strategy in a report took together, which is
// what a caller budgeting a CI run needs.
func TotalDuration(strategies []Strategy) time.Duration {
	var total time.Duration
	for _, s := range strategies {
		total += s.Duration()
	}
	return total
}
