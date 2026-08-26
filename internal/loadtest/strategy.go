package loadtest

import (
	"fmt"
	"sort"
	"time"
)

// A Strategy is a shape of load over time: how many sessions are in flight at
// each moment of a run.
//
// There are several because they answer different questions, and a single
// "fire N requests" number answers none of them well:
//
//   - Steady says what the application sustains. It is the number worth
//     comparing between two builds, because everything else about the run is
//     held still.
//   - Ramp says where it stops sustaining it. Throughput rising with
//     concurrency means headroom; throughput flat while latency climbs means
//     the knee has been passed, and the interesting number is where that
//     happened rather than the peak.
//   - Burst says what an idle system does when traffic arrives all at once,
//     which on Lambda is mostly a question about cold starts and is invisible
//     to a run that warms up first.
//   - Drain says how long the asynchronous half takes to catch up after the
//     synchronous half has stopped. For an application whose real work happens
//     after the response is sent, request latency measures the wrong thing
//     entirely.
type Strategy interface {
	// Name is the identifier used in reports.
	Name() string
	// Describe is one line on what question this answers.
	Describe() string
	// Duration is how long the run lasts.
	Duration() time.Duration
	// Concurrency is how many sessions should be in flight at an offset into
	// the run.
	Concurrency(elapsed time.Duration) int
	// Peak is the highest concurrency this strategy will ask for, so a runner
	// can size its pool once.
	Peak() int
}

// Steady holds concurrency flat for the whole run.
type Steady struct {
	Workers int
	For     time.Duration
}

func (s Steady) Name() string { return "steady" }
func (s Steady) Describe() string {
	return fmt.Sprintf("%d sessions in flight for %s -- what the application sustains",
		s.Workers, s.For)
}
func (s Steady) Duration() time.Duration       { return s.For }
func (s Steady) Concurrency(time.Duration) int { return s.Workers }
func (s Steady) Peak() int                     { return s.Workers }

// Ramp steps concurrency up in equal stages, looking for the point where
// throughput stops rising.
type Ramp struct {
	From, To int
	Stages   int
	PerStage time.Duration
}

func (r Ramp) Name() string { return "ramp" }
func (r Ramp) Describe() string {
	return fmt.Sprintf("%d to %d sessions in %d stages of %s -- where throughput stops rising",
		r.From, r.To, r.Stages, r.PerStage)
}
func (r Ramp) Duration() time.Duration { return r.PerStage * time.Duration(r.Stages) }
func (r Ramp) Peak() int               { return r.To }

func (r Ramp) Concurrency(elapsed time.Duration) int {
	if r.Stages <= 1 {
		return r.To
	}
	stage := int(elapsed / r.PerStage)
	if stage >= r.Stages {
		stage = r.Stages - 1
	}
	span := r.To - r.From
	return r.From + span*stage/(r.Stages-1)
}

// Burst is idle, then everything at once, then idle again.
type Burst struct {
	Workers int
	Quiet   time.Duration
	Spike   time.Duration
	Settle  time.Duration
}

func (b Burst) Name() string { return "burst" }
func (b Burst) Describe() string {
	return fmt.Sprintf("idle %s, then %d sessions at once for %s -- what a cold system does",
		b.Quiet, b.Workers, b.Spike)
}
func (b Burst) Duration() time.Duration { return b.Quiet + b.Spike + b.Settle }
func (b Burst) Peak() int               { return b.Workers }

func (b Burst) Concurrency(elapsed time.Duration) int {
	if elapsed < b.Quiet || elapsed >= b.Quiet+b.Spike {
		return 0
	}
	return b.Workers
}

// Drain sends a fixed amount of work and then stops, so that what is measured
// afterwards is how long the asynchronous half takes to catch up.
//
// The load phase is deliberately short: the question is not how fast requests
// are served but how far behind the pipeline falls and how quickly it
// recovers, and a long load phase only makes the backlog bigger without
// telling you anything new.
type Drain struct {
	Workers int
	Load    time.Duration
}

func (d Drain) Name() string { return "drain" }
func (d Drain) Describe() string {
	return fmt.Sprintf("%d sessions for %s, then measure how long the asynchronous "+
		"half takes to catch up", d.Workers, d.Load)
}
func (d Drain) Duration() time.Duration { return d.Load }
func (d Drain) Peak() int               { return d.Workers }
func (d Drain) Concurrency(elapsed time.Duration) int {
	if elapsed >= d.Load {
		return 0
	}
	return d.Workers
}

// Strategies returns the named strategies at a given scale.
//
// Scale multiplies concurrency and duration together so that one flag moves a
// run between "quick enough for CI" and "long enough to mean something",
// without every strategy needing its own set of knobs.
func Strategies(scale float64) []Strategy {
	if scale <= 0 {
		scale = 1
	}
	n := func(base int) int {
		out := int(float64(base) * scale)
		if out < 1 {
			return 1
		}
		return out
	}
	d := func(base time.Duration) time.Duration {
		out := time.Duration(float64(base) * scale)
		if out < time.Second {
			return time.Second
		}
		return out
	}

	return []Strategy{
		Steady{Workers: n(8), For: d(10 * time.Second)},
		Ramp{From: n(1), To: n(24), Stages: 6, PerStage: d(3 * time.Second)},
		Burst{Workers: n(24), Quiet: d(2 * time.Second), Spike: d(5 * time.Second), Settle: d(2 * time.Second)},
		Drain{Workers: n(6), Load: d(5 * time.Second)},
	}
}

// StrategyByName returns one strategy at a given scale.
func StrategyByName(name string, scale float64) (Strategy, error) {
	for _, s := range Strategies(scale) {
		if s.Name() == name {
			return s, nil
		}
	}
	var names []string
	for _, s := range Strategies(1) {
		names = append(names, s.Name())
	}
	sort.Strings(names)
	return nil, fmt.Errorf("no strategy %q; there is %v", name, names)
}
