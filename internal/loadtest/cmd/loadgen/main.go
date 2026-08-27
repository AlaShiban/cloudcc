// Command loadgen derives a load plan from a compiled application's IR, drives
// it, and reports throughput and connectedness.
//
// It is deliberately a separate binary from cloudcc: putting load on a running
// application is not part of compiling one, and the compile path is checked to
// hold no networking code at all.
//
//	loadgen -ir ir.json -seed scenario.json -url http://127.0.0.1:8099 \
//	        -mode compiled -out report.json
//	loadgen -plan -ir ir.json -seed scenario.json
//	loadgen -compare before.json after.json
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cloudcompiler/cloudcc/internal/loadtest"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "loadgen: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		irPath   = flag.String("ir", "", "a `cloudcc --dump-ir` document")
		seedPath = flag.String("seed", "", "a scenario file supplying request bodies")
		url      = flag.String("url", "", "base URL of the running application")
		app      = flag.String("app", "", "application name, for the report")
		mode     = flag.String("mode", "compiled", `"uncompiled" or "compiled"`)
		out      = flag.String("out", "", "write the JSON report here")
		only     = flag.String("strategy", "", "run one strategy instead of all of them")
		scale    = flag.Float64("scale", 1, "multiply every strategy's concurrency and duration")
		planOnly = flag.Bool("plan", false, "print the derived plan and exit")
		compare  = flag.Bool("compare", false, "compare two report files: -compare before.json after.json")
		attach   = flag.String("attach", "", "a report to attach a connectedness checklist to, then rewrite")
		observed = flag.String("observed", "", "a JSON object of edge -> observed count, to fill in the checklist")
	)
	flag.Parse()

	if *compare {
		return compareReports(flag.Args())
	}
	if *attach != "" {
		return attachChecklist(*attach, *observed, *out)
	}

	if *irPath == "" {
		return fmt.Errorf("-ir is required")
	}
	program, err := readProgram(*irPath)
	if err != nil {
		return err
	}
	seed, err := readSeed(*seedPath)
	if err != nil {
		return err
	}

	name := *app
	if name == "" {
		name = "application"
	}
	plan, err := loadtest.DerivePlan(name, program, seed)
	if err != nil {
		return err
	}

	if *planOnly {
		return writeJSON(os.Stdout, plan)
	}
	if *url == "" {
		return fmt.Errorf("-url is required unless -plan is given")
	}

	strategies := loadtest.Strategies(*scale)
	if *only != "" {
		one, err := loadtest.StrategyByName(*only, *scale)
		if err != nil {
			return err
		}
		strategies = []loadtest.Strategy{one}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	report := &loadtest.Report{App: name, Mode: *mode, Unit: plan.Unit, Plan: plan}
	runner := &loadtest.Runner{BaseURL: *url, Plan: plan}

	for _, strategy := range strategies {
		fmt.Fprintf(os.Stderr, "  %-7s %s\n", strategy.Name(), strategy.Describe())
		metrics, err := runner.Run(ctx, strategy)
		if err != nil {
			return fmt.Errorf("%s: %w", strategy.Name(), err)
		}
		report.Runs = append(report.Runs, metrics.Summarise(strategy, plan))
		if ctx.Err() != nil {
			return fmt.Errorf("interrupted after %s", strategy.Name())
		}
		// A short gap between strategies, so a burst is measuring a system that
		// has actually gone quiet rather than the tail of the previous run.
		select {
		case <-time.After(time.Second):
		case <-ctx.Done():
		}
	}

	fmt.Print(loadtest.RenderRuns(report))
	if len(report.Connected) > 0 {
		fmt.Print(loadtest.RenderConnectedness(report))
	}

	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			return err
		}
		defer f.Close()
		return writeJSON(f, report)
	}
	return nil
}

// attachChecklist adds a connectedness checklist to a report that has already
// been written, and rewrites it.
//
// A separate step because the two happen at different times: the load runs
// first, and the evidence can only be gathered once the asynchronous half has
// caught up. Doing it in one pass would mean either checking too early or
// holding the process open through a sleep.
//
// The counts come from outside because they are AWS queries -- a table scan, a
// bucket listing, a log group's streams -- and the harness that already speaks
// to the emulator is the right place for those. This binary owns what must be
// true; the shell owns how to look.
func attachChecklist(reportPath, observedPath, out string) error {
	report, err := readReport(reportPath)
	if err != nil {
		return err
	}
	if report.Plan == nil {
		return fmt.Errorf("%s carries no plan, so there is nothing to check against", reportPath)
	}
	// The checklist below reads state out of the emulator, and state outlives
	// the run that wrote it. Attaching one to a run that never reached the
	// application would credit it with a previous run's rows.
	if why := report.Undelivered(); why != "" {
		return fmt.Errorf("%s is void: %s", reportPath, why)
	}

	counts := map[string]int{}
	if observedPath != "" {
		data, err := os.ReadFile(observedPath)
		if err != nil {
			return fmt.Errorf("reading observations: %w", err)
		}
		if err := json.Unmarshal(data, &counts); err != nil {
			return fmt.Errorf("reading observations: %w", err)
		}
	}

	for _, want := range report.Plan.Expect {
		count, seen := counts[want.Edge]
		if want.Kind == "http" {
			// This one is answered by the run itself rather than by AWS: a
			// locally served unit has no gateway in front of it, and taking the
			// count from the requests that actually succeeded is the difference
			// between a check and a line that always passes.
			count, seen = successfulRequests(report), true
		}

		observation := loadtest.Observation{
			Edge:     want.Edge,
			Kind:     want.Kind,
			Observed: count,
			Why:      want.Why,
		}
		// A store's evidence is its contents, and contents cannot be attributed
		// to a unit: two units sharing a table means one unit's writes would
		// otherwise credit the other's edge. So a store edge counts as carried
		// only when the unit at its source also ran.
		// Only an edge whose evidence is somebody else's state needs a
		// corroborating signal. An invocations edge counts the unit itself, and
		// an http edge counts the run: those are their own evidence, and
		// running them through the store reasoning produced sentences about
		// rows for edges that have no rows.
		corroborated := want.Fallback != "" && want.Kind != "invocations" && want.Kind != "http"
		sourceRan := !corroborated || counts[want.Edge+unitSuffix] > 0
		if want.Fallback == report.Unit && successfulRequests(report) > 0 {
			// The unit behind the gateway is served locally by the harness, the
			// same way every other test here serves it, so it leaves no Lambda
			// log streams however much traffic it handles. It plainly ran: the
			// load went through it.
			sourceRan = true
		}

		switch {
		case want.Kind == "secret":
			// Not a gap in the run: fetching a secret leaves no trace the
			// emulator exposes, and inventing one would mean asserting on
			// something nothing produces.
			observation.State = loadtest.StateUnverified
			observation.Why = fmt.Sprintf(
				"reading secret %q leaves no trace this harness can see. Where the "+
					"value is used for something observable -- a signed record written to "+
					"a store -- that write is the evidence, under its own edge", want.Target)

		case !seen:
			observation.State = loadtest.StateUnverified
			observation.Why = "nothing looked at this edge. " + want.Why

		case count > 0 && sourceRan:
			observation.State = loadtest.StateCarried

		case !want.SourceReachable:
			// Not a defect: nothing in the application invokes this unit, so
			// no amount of HTTP load can reach it. Blaming the application for
			// a limit of the harness would train a reader to ignore the list.
			observation.State = loadtest.StateUnverified
			observation.Why = fmt.Sprintf(
				"no HTTP request can reach unit %q: nothing in this application calls it "+
					"and no topic it subscribes to is published to from the gateway. A load "+
					"test that speaks HTTP cannot exercise this edge",
				sourceOf(want))

		case count > 0 && !sourceRan:
			observation.State = loadtest.StateUnverified
			observation.Why = fmt.Sprintf(
				"%q holds %d item(s) but unit %q never ran, so those came from a unit "+
					"that shares it. Nothing here shows this edge carrying anything",
				want.Target, count, want.Fallback)

		case corroborated && !sourceRan:
			// The store is empty and so is the unit. Blaming this edge would
			// spread one cause across every store the unit holds; the edge that
			// should have invoked it is the one to look at, and it is reported
			// on its own line.
			observation.State = loadtest.StateUnverified
			observation.Why = fmt.Sprintf(
				"unit %q never ran, so nothing could have reached %q. Whatever should "+
					"have invoked that unit is the edge to look at",
				want.Fallback, want.Target)

		case corroborated && sourceRan:
			// The store is empty and the unit that uses it ran. A unit that
			// only reads a table leaves it empty, and an emulator that serves
			// no read counters cannot tell that from a write path nothing
			// took. Calling it dead would be a false accusation; calling it
			// fine would be a check that passes without looking.
			observation.State = loadtest.StateUnverified
			observation.Why = fmt.Sprintf(
				"%q holds no rows, and unit %q did run (%d invocations). That is what a "+
					"read-only path looks like and also what a dead write path looks like; "+
					"the emulator serves no read counters, so this harness cannot tell them "+
					"apart. Check it by hand, or give the unit a write to make it observable",
				want.Target, want.Fallback, counts[want.Edge+unitSuffix])

		default:
			observation.State = loadtest.StateDead
		}
		observation.OK = observation.State != loadtest.StateDead
		report.Connected = append(report.Connected, observation)
	}

	fmt.Print(loadtest.RenderConnectedness(report))

	target := out
	if target == "" {
		target = reportPath
	}
	f, err := os.Create(target)
	if err != nil {
		return err
	}
	defer f.Close()
	return writeJSON(f, report)
}

// sourceOf names the unit at an edge's source, for a diagnostic.
func sourceOf(want loadtest.Evidence) string {
	if want.Fallback != "" {
		return want.Fallback
	}
	return want.Target
}

// unitSuffix keys the companion count an observer supplies alongside a store
// edge: how many times the unit at the edge's source was invoked. It is what
// separates "this store was never reached" from "this store is only ever read".
const unitSuffix = "#unit"

// successfulRequests is how many requests across every strategy came back
// under 400.
func successfulRequests(report *loadtest.Report) int {
	total := 0
	for _, run := range report.Runs {
		for code, n := range run.ByStatus {
			if len(code) > 0 && code[0] < '4' {
				total += n
			}
		}
	}
	return total
}

func compareReports(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("-compare takes two report files: before.json after.json")
	}
	before, err := readReport(args[0])
	if err != nil {
		return err
	}
	after, err := readReport(args[1])
	if err != nil {
		return err
	}
	fmt.Print(loadtest.RenderComparison(before, after))

	// Before anything is concluded from the edges. A run whose traffic never
	// arrived still finds whatever a previous run left in the emulator, and
	// would otherwise report every edge as carried -- which is how
	// examples/mixed once passed this suite at 0% ok.
	for _, r := range []*loadtest.Report{before, after} {
		if why := r.Undelivered(); why != "" {
			return fmt.Errorf("the %s run is void: %s", r.Mode, why)
		}
	}

	// Printed whether or not anything failed: an edge nobody could settle is
	// a gap in the evidence, and a gap that is only mentioned on failure is
	// one that never gets closed.
	if unverified := after.UnverifiedEdges(); len(unverified) > 0 {
		fmt.Printf("\n%d edge(s) could not be settled either way:\n", len(unverified))
		for _, o := range unverified {
			fmt.Printf("  %s\n       %s\n", o.Edge, o.Why)
		}
	}

	if dead := after.DeadEdges(); len(dead) > 0 {
		fmt.Printf("\n%d edge(s) carried nothing:\n", len(dead))
		for _, o := range dead {
			fmt.Printf("  %s\n       %s\n", o.Edge, o.Why)
		}
		return fmt.Errorf("the compiled application has dead edges")
	}
	return nil
}

func readProgram(path string) (*loadtest.Program, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading the IR: %w", err)
	}
	var p loadtest.Program
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("reading the IR: %w", err)
	}
	return &p, nil
}

func readSeed(path string) (loadtest.Seed, error) {
	if path == "" {
		return loadtest.Seed{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return loadtest.Seed{}, fmt.Errorf("reading the seed: %w", err)
	}
	// A scenario file may put the load requests under "load" so that they can
	// differ from the ones the differential suite replays; otherwise its own
	// requests are the seed.
	var doc struct {
		Requests []loadtest.SeedRequest `json:"requests"`
		Load     *loadtest.Seed         `json:"load"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return loadtest.Seed{}, fmt.Errorf("reading the seed: %w", err)
	}
	// The "load" block may exist only to carry settings -- a scale ceiling, the
	// engines to start -- and still have no requests of its own. Preferring it
	// unconditionally silently emptied the seed, which meant every request with
	// a body went out without one: a fifth of the run came back 422 and the
	// stores it should have written stayed empty, which then read as three dead
	// edges.
	if doc.Load != nil && len(doc.Load.Requests) > 0 {
		return *doc.Load, nil
	}
	return loadtest.Seed{Requests: doc.Requests}, nil
}

func readReport(path string) (*loadtest.Report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var r loadtest.Report
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return &r, nil
}

func writeJSON(w *os.File, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
