// Package loadtest drives an application under load and checks that the wiring
// the compiler described is the wiring that actually carries traffic.
//
// There are two questions here and they are not the same one.
//
// *Throughput* is what a load test usually measures: how many requests a second
// the thing serves, and at what latency. That number is only worth having
// alongside the second question.
//
// *Connectedness* is whether every edge the compiler resolved carried something.
// A compiled application is a graph -- units, stores, topics, calls between
// units -- and the failure this project is organised against is an edge that
// looks right in the plan and is dead at runtime. A store nothing wrote to, a
// topic whose subscriber never woke, a unit nobody invoked: each of those is a
// green deploy and a broken application, and none of them shows up as an error.
// Under load they show up as an absence, which is why the two are measured
// together.
//
// The plan is derived from the compiler's own IR rather than written by hand.
// That is what keeps it honest as an application changes: a route added to a
// program is a route this exercises, and a store added to it is a store this
// expects to see written.
package loadtest

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Program is the part of a `cloudcc --dump-ir` document this package reads.
type Program struct {
	Intents []Intent `json:"intents"`
	Edges   []Edge   `json:"edges"`
}

// Intent is one capability the program declared.
type Intent struct {
	Key     Key             `json:"key"`
	Payload json.RawMessage `json:"payload"`
}

// Key identifies an intent or a resource.
type Key struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

func (k Key) String() string { return k.Kind + "/" + k.ID }

// Edge is one relationship between two nodes.
type Edge struct {
	From Key    `json:"from"`
	To   Key    `json:"to"`
	Kind string `json:"kind"`
}

// Route is one HTTP route discovered on an exposed application.
type Route struct {
	Verb string `json:"verb"`
	Path string `json:"path"`
}

type exposePayload struct {
	ID     string  `json:"id"`
	Unit   string  `json:"unit"`
	Routes []Route `json:"routes"`
}

// Capability kinds this package reasons about. Spelled here rather than
// imported so that the load harness reads a dumped document rather than
// depending on the compiler's internals.
const (
	kindExpose     = "expose"
	kindUnit       = "execution_unit"
	edgeUses       = "uses"
	edgePublishes  = "publishes"
	edgeSubscribes = "subscribes"
	edgeCalls      = "calls"
)

// Phase is where a step sits in a session. Ordering matters: a read that runs
// before the write it depends on measures the latency of a 404, which is a
// number with no relationship to the one being asked for.
type Phase int

const (
	// PhaseCreate writes something and, where the id is chosen by the server,
	// is where it becomes known.
	PhaseCreate Phase = iota
	// PhaseRead is every safe request.
	PhaseRead
	// PhaseDelete removes what the session created, so a long run does not
	// measure a store that grows without bound.
	PhaseDelete
)

func (p Phase) String() string {
	switch p {
	case PhaseCreate:
		return "create"
	case PhaseRead:
		return "read"
	case PhaseDelete:
		return "delete"
	}
	return "?"
}

// Step is one request shape in a session.
type Step struct {
	Verb string          `json:"verb"`
	Path string          `json:"path"`
	Body json.RawMessage `json:"body,omitempty"`
	// Phase decides when in a session this runs.
	Phase Phase `json:"-"`
	// PhaseName is the phase, for the report.
	PhaseName string `json:"phase"`
	// Param is the path parameter this step's path carries, if any.
	Param string `json:"param,omitempty"`
}

// Evidence is what must be observably true after a run for one edge to count
// as connected.
type Evidence struct {
	// Edge is the relationship being checked, for the report.
	Edge string `json:"edge"`
	// Kind is how to look: "http", "store", "bucket", "invocations".
	Kind string `json:"kind"`
	// Target is the capability id whose state proves it.
	Target string `json:"target"`
	// SourceReachable is whether HTTP load can reach the unit at the edge's
	// source at all, following calls and topic deliveries out from the gateway.
	//
	// A unit nothing invokes cannot be exercised by a load test that speaks
	// HTTP, so an absence of evidence for it says nothing about the wiring --
	// it says the plan has no way in. Calling that dead would be blaming the
	// application for a limit of the harness.
	SourceReachable bool `json:"source_reachable"`
	// Fallback names the unit whose having been invoked would explain an
	// absence of evidence.
	//
	// A store's evidence is the rows in it, which detects a write and cannot
	// detect a read: a unit that only ever reads a table leaves it empty, and
	// an empty table looks exactly like one nothing reached. The emulator
	// offers no read counter to tell them apart. So when the rows are absent
	// but the unit itself ran, the honest answer is "unverified" with the
	// reason -- not "dead", which would be a false accusation, and not "ok",
	// which would be a check that passes without looking.
	Fallback string `json:"fallback,omitempty"`
	// Why explains, in the report, what a failure would mean.
	Why string `json:"why"`
}

// Plan is a derived description of what to send and what must follow.
type Plan struct {
	App string `json:"app"`
	// Unit is the execution unit that serves HTTP.
	Unit string `json:"unit"`
	// Steps are the request shapes, already ordered by phase.
	Steps []Step `json:"steps"`
	// Expect is the connectedness checklist.
	Expect []Evidence `json:"expect"`
	// Routes is every route the program declared, so the report can say what
	// share of them the plan reaches.
	Routes []Route `json:"routes"`
}

// Seed supplies the request bodies a plan cannot derive.
//
// Everything structural comes from the IR: which routes exist, which verbs they
// answer, which parameters they take, which stores and topics must show
// evidence. What the IR does not describe is what a *body* has to contain --
// `POST /orders` needs items in it, and no amount of reading the graph will say
// so. Those come from the scenario files the differential suite already uses,
// which is the one place that knowledge already lives; inventing a second one
// would mean maintaining the same facts twice.
type Seed struct {
	Requests []SeedRequest `json:"requests"`
}

// SeedRequest is one example request from a scenario file.
type SeedRequest struct {
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Body   json.RawMessage `json:"body,omitempty"`
}

// DerivePlan builds a plan for an application from its IR and a body seed.
func DerivePlan(app string, p *Program, seed Seed) (*Plan, error) {
	exposed, err := exposedUnit(p)
	if err != nil {
		return nil, err
	}

	plan := &Plan{App: app, Unit: exposed.Unit, Routes: exposed.Routes}
	for _, route := range exposed.Routes {
		step := Step{
			Verb:  strings.ToUpper(route.Verb),
			Path:  route.Path,
			Phase: phaseOf(route),
			Param: paramOf(route.Path),
		}
		step.PhaseName = step.Phase.String()
		if body, ok := bodyFor(route, seed); ok {
			step.Body = body
		}
		plan.Steps = append(plan.Steps, step)
	}

	// Stable, and in the order a session runs them.
	sort.SliceStable(plan.Steps, func(i, j int) bool {
		if plan.Steps[i].Phase != plan.Steps[j].Phase {
			return plan.Steps[i].Phase < plan.Steps[j].Phase
		}
		return plan.Steps[i].Path < plan.Steps[j].Path
	})

	plan.Expect = expectations(p, exposed)
	return plan, nil
}

// exposedUnit returns the gateway that fronts the application.
func exposedUnit(p *Program) (exposePayload, error) {
	var found []exposePayload
	for _, in := range p.Intents {
		if in.Key.Kind != kindExpose {
			continue
		}
		var payload exposePayload
		if err := json.Unmarshal(in.Payload, &payload); err != nil {
			return exposePayload{}, fmt.Errorf("reading expose %s: %w", in.Key.ID, err)
		}
		found = append(found, payload)
	}
	switch len(found) {
	case 0:
		return exposePayload{}, fmt.Errorf(
			"this application exposes nothing over HTTP, so there is no way to put load on it")
	case 1:
		return found[0], nil
	}
	sort.Slice(found, func(i, j int) bool { return found[i].ID < found[j].ID })
	// More than one gateway is legitimate; driving the first by sorted id is a
	// choice, and saying which one was driven is the report's job.
	return found[0], nil
}

// phaseOf classifies a route by its verb. GET and HEAD are safe, DELETE
// removes, and everything else writes.
func phaseOf(r Route) Phase {
	switch strings.ToUpper(r.Verb) {
	case "GET", "HEAD", "OPTIONS":
		return PhaseRead
	case "DELETE":
		return PhaseDelete
	}
	return PhaseCreate
}

// paramOf returns the single path parameter in a route, or "".
//
// One is enough for every route in this repository, and a route with two would
// need the plan to know how they relate -- which is a question about the
// application rather than about its shape, so it is reported rather than
// guessed at.
func paramOf(path string) string {
	open := strings.Index(path, "{")
	if open < 0 {
		return ""
	}
	closed := strings.Index(path[open:], "}")
	if closed < 0 {
		return ""
	}
	return path[open+1 : open+closed]
}

// bodyFor finds a seed body for a route, matching on method and on the route's
// shape rather than on the exact path -- a seed written for /pets/1 supplies
// the body for /pets/{pet_id}.
func bodyFor(route Route, seed Seed) (json.RawMessage, bool) {
	verb := strings.ToUpper(route.Verb)
	for _, req := range seed.Requests {
		if strings.ToUpper(req.Method) != verb || len(req.Body) == 0 {
			continue
		}
		if sameShape(route.Path, req.Path) {
			return req.Body, true
		}
	}
	return nil, false
}

// sameShape reports whether a concrete path could have come from a templated
// one: /pets/{pet_id} and /pets/1 have the same shape, /pets and /pets/1 do not.
func sameShape(template, concrete string) bool {
	t := strings.Split(strings.Trim(template, "/"), "/")
	c := strings.Split(strings.Trim(concrete, "/"), "/")
	if len(t) != len(c) {
		return false
	}
	for i := range t {
		if strings.HasPrefix(t[i], "{") {
			continue
		}
		if t[i] != c[i] {
			return false
		}
	}
	return true
}

// expectations turns the graph into a checklist.
//
// Every edge that carries data at runtime gets an entry. Edges that describe
// how the project is built rather than what happens in it -- resolves_to,
// depends_on -- are not runtime facts and are left out; so is config, which is
// an environment variable rather than something traffic flows through.
//
// `publishes` has no entry of its own, and that is a limit worth naming rather
// than papering over. What proves a publish happened is a subscriber that woke,
// which is already checked from the other end -- so a topic *with* subscribers
// is covered twice and a topic with none cannot be verified from outside at
// all. Counting published messages would need a metric the emulator does not
// serve, and asserting on a number nothing produces would be worse than saying
// so here.
func expectations(p *Program, exposed exposePayload) []Evidence {
	var out []Evidence
	seen := map[string]bool{}
	add := func(e Evidence) {
		if seen[e.Edge] {
			return
		}
		seen[e.Edge] = true
		out = append(out, e)
	}

	reachable := reachableUnits(p, exposed.Unit)

	add(Evidence{
		Edge:            kindExpose + "/" + exposed.ID + " -exposes-> " + kindUnit + "/" + exposed.Unit,
		Kind:            "http",
		Target:          exposed.Unit,
		SourceReachable: true,
		Why: "the gateway answered nothing at all, so no other edge below could " +
			"have been reached either",
	})

	for _, e := range p.Edges {
		if e.From.Kind != kindUnit {
			continue
		}
		label := e.From.String() + " -" + e.Kind + "-> " + e.To.String()
		switch {
		case e.Kind == edgeUses && strings.HasPrefix(e.To.Kind, "persist_"):
			kind := "store"
			if e.To.Kind == "persist_fs" {
				kind = "bucket"
			}
			add(Evidence{
				Edge: label, Kind: kind, Target: e.To.ID, Fallback: e.From.ID,
				SourceReachable: reachable[e.From.ID],
				Why: fmt.Sprintf("unit %q declares store %q and the compiler wired it, "+
					"but nothing reached it under load -- either the code path never runs "+
					"or the binding is dead", e.From.ID, e.To.ID),
			})
		case e.Kind == edgeCalls:
			add(Evidence{
				Edge: label, Kind: "invocations", Target: e.To.ID,
				SourceReachable: reachable[e.From.ID],
				Why: fmt.Sprintf("unit %q calls unit %q over the wire, and unit %q was "+
					"never invoked -- the call is compiled but no request reaches it",
					e.From.ID, e.To.ID, e.To.ID),
			})
		case e.Kind == edgeSubscribes:
			add(Evidence{
				Edge: label, Kind: "invocations", Target: e.From.ID,
				SourceReachable: reachable[e.From.ID],
				Why: fmt.Sprintf("unit %q subscribes to topic %q and was never invoked, "+
					"so either nothing published or the subscription is not delivering",
					e.From.ID, e.To.ID),
			})
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Edge < out[j].Edge })
	return out
}

// RouteCoverage is the share of declared routes a plan reaches, which is what
// makes "exercises most of the application" a number rather than a claim.
func (p *Plan) RouteCoverage() (reached, total int) {
	declared := map[string]bool{}
	for _, r := range p.Routes {
		declared[strings.ToUpper(r.Verb)+" "+r.Path] = true
	}
	hit := map[string]bool{}
	for _, s := range p.Steps {
		key := s.Verb + " " + s.Path
		if declared[key] {
			hit[key] = true
		}
	}
	return len(hit), len(declared)
}

// reachableUnits returns the units HTTP load can actually reach, starting from
// the one behind the gateway and following the two ways a unit reaches another:
// a call, and a message.
//
// A unit outside this set is not a defect and not a dead edge -- it is a unit
// with no way in from the network, which a load test that speaks HTTP cannot
// exercise however hard it tries. examples/mixed has one: a worker whose
// function nothing invokes.
func reachableUnits(p *Program, entry string) map[string]bool {
	// topic -> the units that subscribe to it.
	subscribers := map[string][]string{}
	for _, e := range p.Edges {
		if e.Kind == edgeSubscribes && e.From.Kind == kindUnit {
			subscribers[e.To.String()] = append(subscribers[e.To.String()], e.From.ID)
		}
	}

	reached := map[string]bool{entry: true}
	queue := []string{entry}
	for len(queue) > 0 {
		unit := queue[0]
		queue = queue[1:]
		for _, e := range p.Edges {
			if e.From.Kind != kindUnit || e.From.ID != unit {
				continue
			}
			var next []string
			switch e.Kind {
			case edgeCalls:
				next = []string{e.To.ID}
			case edgePublishes:
				next = subscribers[e.To.String()]
			}
			for _, id := range next {
				if !reached[id] {
					reached[id] = true
					queue = append(queue, id)
				}
			}
		}
	}
	return reached
}
