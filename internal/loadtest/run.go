package loadtest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Result is one request's outcome.
type Result struct {
	Verb    string
	Path    string
	Status  int
	Latency time.Duration
	Err     error
}

// Metrics accumulates results and answers the questions a report asks.
//
// Latencies are kept rather than summarised on the way in, because percentiles
// cannot be computed from a running mean and the tail is the part worth
// looking at. At the volumes this harness runs -- tens of thousands, not
// millions -- a slice is the simple answer.
type Metrics struct {
	mu        sync.Mutex
	latencies []time.Duration
	byStatus  map[int]int
	byRoute   map[string]int
	failures  map[string]int
	started   time.Time
	stopped   time.Time
}

// NewMetrics returns an empty collector.
func NewMetrics() *Metrics {
	return &Metrics{
		byStatus: map[int]int{},
		byRoute:  map[string]int{},
		failures: map[string]int{},
	}
}

// Add records one result.
func (m *Metrics) Add(r Result) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.latencies = append(m.latencies, r.Latency)
	m.byRoute[r.Verb+" "+r.Path]++
	if r.Err != nil {
		m.failures[transportError(r.Err)]++
		return
	}
	m.byStatus[r.Status]++
}

// transportError collapses a client error to something countable. The whole
// message carries a port and an address that differ between runs, and a report
// that lists ten thousand distinct errors is one nobody reads.
func transportError(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "connection refused"):
		return "connection refused"
	case strings.Contains(msg, "connection reset"):
		return "connection reset"
	case strings.Contains(msg, "Client.Timeout"), strings.Contains(msg, "context deadline"):
		return "timeout"
	case strings.Contains(msg, "EOF"):
		return "unexpected EOF"
	}
	return "other transport error"
}

// Summary is the numeric result of one run.
type Summary struct {
	Strategy    string         `json:"strategy"`
	Describes   string         `json:"describes"`
	Requests    int            `json:"requests"`
	Duration    string         `json:"duration"`
	Throughput  float64        `json:"requests_per_second"`
	P50         string         `json:"p50"`
	P90         string         `json:"p90"`
	P99         string         `json:"p99"`
	Max         string         `json:"max"`
	ByStatus    map[string]int `json:"by_status"`
	Failures    map[string]int `json:"failures,omitempty"`
	OKRate      float64        `json:"ok_rate"`
	RoutesHit   int            `json:"routes_hit"`
	RoutesTotal int            `json:"routes_total"`
	// LowSample marks a run with too few requests for its numbers to mean
	// much. A throughput computed from four requests and one from five
	// thousand print identically, and only one of them is worth acting on --
	// an application whose sessions take about as long as the whole run
	// produces the first without anything being wrong.
	LowSample bool `json:"low_sample,omitempty"`
}

// minMeaningfulSample is where a run stops being an anecdote. Chosen to be
// obviously too small rather than statistically derived: the point is to stop a
// reader trusting a number, not to promise that a larger one is trustworthy.
const minMeaningfulSample = 30

// Summarise computes the run's numbers.
func (m *Metrics) Summarise(strategy Strategy, plan *Plan) Summary {
	m.mu.Lock()
	defer m.mu.Unlock()

	sorted := append([]time.Duration(nil), m.latencies...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	elapsed := m.stopped.Sub(m.started)
	if elapsed <= 0 {
		elapsed = time.Millisecond
	}

	ok := 0
	byStatus := map[string]int{}
	for code, n := range m.byStatus {
		byStatus[fmt.Sprintf("%d", code)] = n
		if code < 400 {
			ok += n
		}
	}

	total := len(sorted)
	rate := 0.0
	if total > 0 {
		rate = float64(ok) / float64(total)
	}

	hit, declared := plan.RouteCoverage()
	// Routes actually reached during the run, which can be fewer than the plan
	// intended if a phase never ran.
	reached := 0
	for _, r := range plan.Routes {
		if m.byRoute[strings.ToUpper(r.Verb)+" "+r.Path] > 0 {
			reached++
		}
	}
	_ = hit

	failures := map[string]int{}
	for k, v := range m.failures {
		failures[k] = v
	}
	if len(failures) == 0 {
		failures = nil
	}

	return Summary{
		Strategy:    strategy.Name(),
		Describes:   strategy.Describe(),
		Requests:    total,
		Duration:    elapsed.Round(time.Millisecond).String(),
		Throughput:  float64(total) / elapsed.Seconds(),
		P50:         percentile(sorted, 0.50).Round(time.Microsecond).String(),
		P90:         percentile(sorted, 0.90).Round(time.Microsecond).String(),
		P99:         percentile(sorted, 0.99).Round(time.Microsecond).String(),
		Max:         percentile(sorted, 1.0).Round(time.Microsecond).String(),
		ByStatus:    byStatus,
		Failures:    failures,
		OKRate:      rate,
		RoutesHit:   reached,
		RoutesTotal: declared,
		LowSample:   total < minMeaningfulSample,
	}
}

// percentile returns the p-th percentile of a sorted slice, nearest-rank.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p * float64(len(sorted)-1))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// newClient builds an HTTP client that keeps one connection per session alive.
//
// This is not tuning. Go's default client holds two idle connections per host,
// so above two sessions in flight nearly every request opens a fresh TCP
// connection -- and against localhost, tens of thousands of short-lived
// connections a second exhaust the ephemeral port range within seconds. What
// comes back then is "connection refused", from the operating system rather
// than from the application, and the run measures the harness's socket churn
// instead of the thing under test.
//
// It is a measurement bug that looks exactly like a broken application, and it
// hides itself: both halves of a comparison hit the same wall at the same rate,
// so the ratio comes out at a reassuring 1.00x. examples/mixed produced a
// million refused requests and a table of 1.00x ratios before this existed.
func newClient(peak int) *http.Client {
	idle := peak * 2
	if idle < 8 {
		idle = 8
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = idle
	transport.MaxIdleConnsPerHost = idle
	transport.MaxConnsPerHost = idle
	return &http.Client{Timeout: 30 * time.Second, Transport: transport}
}

// Runner drives one plan against one base URL.
type Runner struct {
	BaseURL string
	Plan    *Plan
	Client  *http.Client
	// Notice, when set, receives a line per notable event.
	Notice func(string)
}

// Run executes a strategy and returns its metrics.
//
// Concurrency is adjusted as the strategy asks for it by starting and stopping
// session goroutines, which is a direct expression of "this many in flight"
// rather than a rate limiter approximating it. Each session owns a key of its
// own, so the read it performs is a read of the row it just wrote -- a read of
// somebody else's row would be a different measurement, and a read of a row
// that does not exist would be a measurement of the 404 path.
func (r *Runner) Run(ctx context.Context, strategy Strategy) (*Metrics, error) {
	if r.Client == nil {
		r.Client = newClient(strategy.Peak())
	}
	metrics := NewMetrics()
	metrics.started = time.Now()

	var (
		wg      sync.WaitGroup
		stops   []context.CancelFunc
		counter atomic.Int64
	)
	deadline := time.After(strategy.Duration())
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()

	adjust := func() {
		want := strategy.Concurrency(time.Since(metrics.started))
		for len(stops) < want {
			sessionCtx, cancel := context.WithCancel(ctx)
			stops = append(stops, cancel)
			wg.Add(1)
			go func() {
				defer wg.Done()
				for sessionCtx.Err() == nil {
					r.session(sessionCtx, metrics, int(counter.Add(1)))
				}
			}()
		}
		for len(stops) > want {
			stops[len(stops)-1]()
			stops = stops[:len(stops)-1]
		}
	}
	adjust()

loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-deadline:
			break loop
		case <-tick.C:
			adjust()
		}
	}

	for _, stop := range stops {
		stop()
	}
	wg.Wait()
	metrics.stopped = time.Now()
	return metrics, nil
}

// session runs one pass through the plan with a key of its own.
func (r *Runner) session(ctx context.Context, metrics *Metrics, n int) {
	key := fmt.Sprintf("lt-%d-%d", n, time.Now().UnixNano()%1000)
	// A server that chooses the id tells us in its reply; a path parameter we
	// supply is ours from the start.
	captured := map[string]string{}

	for _, step := range r.Plan.Steps {
		if ctx.Err() != nil {
			return
		}
		path := step.Path
		if step.Param != "" {
			value := key
			if got, ok := captured[step.Param]; ok {
				value = got
			}
			path = strings.ReplaceAll(path, "{"+step.Param+"}", value)
		}

		body, reply := r.send(ctx, metrics, step, path, key)
		_ = body
		if step.Phase == PhaseCreate {
			for name, value := range identifiers(reply) {
				captured[name] = value
			}
		}
	}
}

// send performs one request and records it.
func (r *Runner) send(ctx context.Context, metrics *Metrics, step Step, path, key string) ([]byte, map[string]any) {
	var payload io.Reader
	if len(step.Body) > 0 {
		payload = bytes.NewReader(withKey(step.Body, key))
	}

	req, err := http.NewRequestWithContext(ctx, step.Verb, r.BaseURL+path, payload)
	if err != nil {
		metrics.Add(Result{Verb: step.Verb, Path: step.Path, Err: err})
		return nil, nil
	}
	if payload != nil {
		req.Header.Set("content-type", "application/json")
	}

	start := time.Now()
	resp, err := r.Client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			// A session cancelled at the end of a run is not a failure of the
			// application, and counting it as one would make every strategy
			// report an error rate proportional to its concurrency.
			return nil, nil
		}
		metrics.Add(Result{Verb: step.Verb, Path: step.Path, Latency: time.Since(start), Err: err})
		return nil, nil
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	metrics.Add(Result{Verb: step.Verb, Path: step.Path, Status: resp.StatusCode, Latency: time.Since(start)})

	var decoded map[string]any
	_ = json.Unmarshal(data, &decoded)
	return data, decoded
}

// withKey stamps a session's key into a seed body wherever the body carries an
// id-like field, so that concurrent sessions do not all write the same row.
func withKey(body json.RawMessage, key string) []byte {
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return body
	}
	for name := range decoded {
		if isIdentifier(name) {
			decoded[name] = key
		}
	}
	out, err := json.Marshal(decoded)
	if err != nil {
		return body
	}
	return out
}

// identifiers picks the id-like fields out of a reply, which is how a session
// learns the key for a resource the server named.
func identifiers(reply map[string]any) map[string]string {
	out := map[string]string{}
	for name, value := range reply {
		text, ok := value.(string)
		if !ok || !isIdentifier(name) {
			continue
		}
		out[name] = text
		// Also under the bare name, since a route's parameter is often spelled
		// differently from the field: {order_id} against "id", or the reverse.
		if trimmed := strings.TrimSuffix(name, "_id"); trimmed != name {
			out[trimmed] = text
		}
		out["id"] = text
	}
	return out
}

func isIdentifier(name string) bool {
	lower := strings.ToLower(name)
	return lower == "id" || strings.HasSuffix(lower, "_id") || strings.HasSuffix(lower, "id")
}
