package loadtest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStrategiesShapeConcurrencyOverTime(t *testing.T) {
	steady := Steady{Workers: 4, For: 2 * time.Second}
	for _, at := range []time.Duration{0, time.Second, 2 * time.Second} {
		if got := steady.Concurrency(at); got != 4 {
			t.Errorf("steady at %s = %d, want 4", at, got)
		}
	}

	// A ramp reaches its ceiling on the last stage and not before, or the
	// "where does throughput stop rising" question is asked at one point.
	ramp := Ramp{From: 2, To: 10, Stages: 5, PerStage: time.Second}
	got := []int{}
	for stage := 0; stage < 5; stage++ {
		got = append(got, ramp.Concurrency(time.Duration(stage)*time.Second+100*time.Millisecond))
	}
	want := []int{2, 4, 6, 8, 10}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ramp = %v, want %v", got, want)
		}
	}

	// A burst is idle either side, which is the whole point: it measures what
	// an application does when traffic arrives at a cold system.
	burst := Burst{Workers: 12, Quiet: time.Second, Spike: 2 * time.Second, Settle: time.Second}
	if burst.Concurrency(500*time.Millisecond) != 0 {
		t.Error("a burst should be idle before the spike")
	}
	if burst.Concurrency(2*time.Second) != 12 {
		t.Error("a burst should be at full concurrency during the spike")
	}
	if burst.Concurrency(3500*time.Millisecond) != 0 {
		t.Error("a burst should be idle after the spike")
	}
	if burst.Duration() != 4*time.Second {
		t.Errorf("burst duration = %s", burst.Duration())
	}
}

func TestScaleMovesEveryStrategyTogether(t *testing.T) {
	small := Strategies(0.25)
	large := Strategies(2)
	if len(small) != len(large) || len(small) == 0 {
		t.Fatalf("strategy sets differ in size: %d and %d", len(small), len(large))
	}
	for i := range small {
		if small[i].Name() != large[i].Name() {
			t.Fatalf("strategy %d is %q at one scale and %q at another",
				i, small[i].Name(), large[i].Name())
		}
		if small[i].Peak() > large[i].Peak() {
			t.Errorf("%s peaks higher at a smaller scale", small[i].Name())
		}
	}
	// Scale must never round a strategy down to nothing; a run of zero
	// sessions reports a throughput of zero and looks like a failure.
	for _, s := range Strategies(0.01) {
		if s.Peak() < 1 || s.Duration() < time.Second {
			t.Errorf("%s degenerates at a small scale: peak %d, duration %s",
				s.Name(), s.Peak(), s.Duration())
		}
	}
}

func TestPercentilesComeFromTheWholeDistribution(t *testing.T) {
	m := NewMetrics()
	m.started = time.Now()
	for i := 1; i <= 100; i++ {
		m.Add(Result{Verb: "GET", Path: "/x", Status: 200, Latency: time.Duration(i) * time.Millisecond})
	}
	m.stopped = m.started.Add(time.Second)

	plan := &Plan{Routes: []Route{{Verb: "GET", Path: "/x"}}}
	s := m.Summarise(Steady{Workers: 1, For: time.Second}, plan)

	if s.Requests != 100 {
		t.Errorf("requests = %d", s.Requests)
	}
	// Nearest-rank: p99 of 1..100ms is the 99th value, not the mean and not
	// the max. A tail reported as an average is the number that hides the
	// problem being looked for.
	if s.P50 != "50ms" || s.P99 != "99ms" || s.Max != "100ms" {
		t.Errorf("p50=%s p99=%s max=%s", s.P50, s.P99, s.Max)
	}
	if s.OKRate != 1 {
		t.Errorf("ok rate = %v", s.OKRate)
	}
}

func TestFailuresAreCountedByKindRatherThanByMessage(t *testing.T) {
	m := NewMetrics()
	m.started = time.Now()
	for i := 0; i < 3; i++ {
		// Distinct messages, one cause: a report listing three thousand
		// addresses is one nobody reads.
		m.Add(Result{Verb: "GET", Path: "/x", Err: fmt.Errorf(
			"Get \"http://127.0.0.1:%d/x\": dial tcp: connection refused", 9000+i)})
	}
	m.Add(Result{Verb: "GET", Path: "/x", Status: 500, Latency: time.Millisecond})
	m.stopped = m.started.Add(time.Second)

	s := m.Summarise(Steady{Workers: 1, For: time.Second}, &Plan{})
	if s.Failures["connection refused"] != 3 {
		t.Errorf("failures = %v", s.Failures)
	}
	if s.ByStatus["500"] != 1 {
		t.Errorf("statuses = %v", s.ByStatus)
	}
	// A 500 is a request that completed, so it counts against the ok rate
	// rather than against the transport.
	if s.OKRate != 0 {
		t.Errorf("ok rate = %v, want 0", s.OKRate)
	}
}

// The runner has to write before it reads, and read the row it just wrote --
// which for a server that names the resource means learning the id from the
// reply. Without that, every read in every run is a 404 and the numbers are
// measurements of the error path.
func TestASessionReadsBackWhatItCreated(t *testing.T) {
	var (
		mu     sync.Mutex
		stored = map[string]bool{}
		reads  int
		misses int
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/orders", func(w http.ResponseWriter, r *http.Request) {
		id := fmt.Sprintf("server-%d", time.Now().UnixNano())
		mu.Lock()
		stored[id] = true
		mu.Unlock()
		w.Header().Set("content-type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"order_id": id})
	})
	mux.HandleFunc("/orders/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/orders/")
		mu.Lock()
		ok := stored[id]
		reads++
		if !ok {
			misses++
		}
		mu.Unlock()
		if !ok {
			http.Error(w, "no such order", http.StatusNotFound)
			return
		}
		w.Write([]byte(`{"ok":true}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	plan := &Plan{
		App:  "t",
		Unit: "storefront",
		Steps: []Step{
			{Verb: "POST", Path: "/orders", Phase: PhaseCreate, PhaseName: "create",
				Body: json.RawMessage(`{"items":[]}`)},
			{Verb: "GET", Path: "/orders/{order_id}", Phase: PhaseRead, PhaseName: "read", Param: "order_id"},
		},
		Routes: []Route{{Verb: "POST", Path: "/orders"}, {Verb: "GET", Path: "/orders/{order_id}"}},
	}

	runner := &Runner{BaseURL: server.URL, Plan: plan}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	metrics, err := runner.Run(ctx, Steady{Workers: 2, For: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	summary := metrics.Summarise(Steady{Workers: 2, For: time.Second}, plan)

	mu.Lock()
	defer mu.Unlock()
	if reads == 0 {
		t.Fatal("no reads happened at all")
	}
	if misses != 0 {
		t.Errorf("%d of %d reads missed; the session did not learn the id the "+
			"server assigned, so every read measured the 404 path", misses, reads)
	}
	if summary.OKRate != 1 {
		t.Errorf("ok rate = %v, statuses %v", summary.OKRate, summary.ByStatus)
	}
	if summary.RoutesHit != 2 {
		t.Errorf("routes hit = %d of %d", summary.RoutesHit, summary.RoutesTotal)
	}
}

// A run that is cancelled at its deadline must not report the sessions it cut
// short as application failures, or every strategy would show an error rate
// proportional to its concurrency.
func TestCancellingAtTheDeadlineIsNotAFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	plan := &Plan{
		Steps:  []Step{{Verb: "GET", Path: "/slow", Phase: PhaseRead, PhaseName: "read"}},
		Routes: []Route{{Verb: "GET", Path: "/slow"}},
	}
	runner := &Runner{BaseURL: server.URL, Plan: plan}
	metrics, err := runner.Run(context.Background(), Steady{Workers: 8, For: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	s := metrics.Summarise(Steady{Workers: 8, For: time.Second}, plan)
	if len(s.Failures) != 0 {
		t.Errorf("cutting a run short reported failures: %v", s.Failures)
	}
	if s.Requests == 0 {
		t.Error("no requests were recorded")
	}
}

// Concurrent sessions must not all write the same row: a load test in which
// every session updates one key measures lock contention on that key rather
// than the application.
func TestSessionsUseKeysOfTheirOwn(t *testing.T) {
	var (
		mu   sync.Mutex
		keys = map[string]bool{}
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		if id, ok := body["id"].(string); ok {
			keys[id] = true
		}
		mu.Unlock()
		w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	plan := &Plan{
		Steps: []Step{{Verb: "PUT", Path: "/items", Phase: PhaseCreate, PhaseName: "create",
			Body: json.RawMessage(`{"id":"seed","name":"x"}`)}},
		Routes: []Route{{Verb: "PUT", Path: "/items"}},
	}
	runner := &Runner{BaseURL: server.URL, Plan: plan}
	if _, err := runner.Run(context.Background(), Steady{Workers: 4, For: time.Second}); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(keys) < 4 {
		t.Errorf("only %d distinct keys were written; sessions are sharing one row", len(keys))
	}
	if keys["seed"] {
		t.Error("the seed body's own id reached the server; it should be replaced per session")
	}
}
