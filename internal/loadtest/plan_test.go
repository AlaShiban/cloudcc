package loadtest

import (
	"encoding/json"
	"strings"
	"testing"
)

// The IR of an application shaped like nomnom: a gateway, a unit that calls two
// others and subscribes to nothing, two stores and a subscriber.
const irJSON = `{
  "intents": [
    {"key": {"kind": "expose", "id": "nomnom-api"},
     "payload": {"id": "nomnom-api", "unit": "storefront", "routes": [
       {"verb": "GET", "path": "/health"},
       {"verb": "POST", "path": "/orders"},
       {"verb": "GET", "path": "/orders/{order_id}"},
       {"verb": "DELETE", "path": "/orders/{order_id}"}
     ]}},
    {"key": {"kind": "execution_unit", "id": "storefront"}, "payload": {"id": "storefront"}},
    {"key": {"kind": "execution_unit", "id": "pricing"}, "payload": {"id": "pricing"}},
    {"key": {"kind": "persist_kv", "id": "orders"}, "payload": {"id": "orders"}}
  ],
  "edges": [
    {"from": {"kind": "expose", "id": "nomnom-api"}, "to": {"kind": "execution_unit", "id": "storefront"}, "kind": "exposes"},
    {"from": {"kind": "execution_unit", "id": "storefront"}, "to": {"kind": "persist_kv", "id": "orders"}, "kind": "uses"},
    {"from": {"kind": "execution_unit", "id": "storefront"}, "to": {"kind": "execution_unit", "id": "pricing"}, "kind": "calls"},
    {"from": {"kind": "execution_unit", "id": "storefront"}, "to": {"kind": "execution_unit", "id": "pricing"}, "kind": "uses"},
    {"from": {"kind": "execution_unit", "id": "notify"}, "to": {"kind": "pubsub", "id": "orderPlaced"}, "kind": "subscribes"},
    {"from": {"kind": "execution_unit", "id": "notify"}, "to": {"kind": "persist_fs", "id": "notifications"}, "kind": "uses"},
    {"from": {"kind": "aws.lambda", "id": "storefront"}, "to": {"kind": "aws.dynamodb", "id": "orders"}, "kind": "uses"},
    {"from": {"kind": "execution_unit", "id": "storefront"}, "to": {"kind": "aws.iam.role", "id": "x"}, "kind": "resolves_to"}
  ]
}`

func testPlan(t *testing.T, seed Seed) *Plan {
	t.Helper()
	var p Program
	if err := json.Unmarshal([]byte(irJSON), &p); err != nil {
		t.Fatal(err)
	}
	plan, err := DerivePlan("nomnom", &p, seed)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

// A read that runs before the write it depends on measures the latency of a
// 404, which is a number with no relationship to the one being asked for.
func TestASessionWritesBeforeItReads(t *testing.T) {
	plan := testPlan(t, Seed{})

	var order []string
	for _, s := range plan.Steps {
		order = append(order, s.PhaseName)
	}
	seenRead := false
	for i, phase := range order {
		if phase == "read" {
			seenRead = true
		}
		if phase == "create" && seenRead {
			t.Errorf("step %d writes after a read: %v", i, order)
		}
		if phase == "delete" && i < len(order)-1 && order[i+1] != "delete" {
			t.Errorf("a delete is not last: %v", order)
		}
	}
}

// Every declared route is exercised. "Most of the application's code paths" is
// only a claim worth making if the plan is derived from the routes rather than
// from whatever somebody remembered to list.
func TestThePlanReachesEveryDeclaredRoute(t *testing.T) {
	plan := testPlan(t, Seed{})
	reached, total := plan.RouteCoverage()
	if total != 4 {
		t.Fatalf("expected 4 declared routes, got %d", total)
	}
	if reached != total {
		t.Errorf("the plan reaches %d of %d routes", reached, total)
	}
}

// The structural half is derived; only bodies come from the seed, and they are
// matched by shape so that a seed written for /orders/1 supplies the body for
// /orders/{order_id}.
func TestBodiesComeFromTheSeedByShape(t *testing.T) {
	seed := Seed{Requests: []SeedRequest{
		{Method: "POST", Path: "/orders", Body: json.RawMessage(`{"items":[{"sku":"cola"}]}`)},
		{Method: "GET", Path: "/orders/1"},
	}}
	plan := testPlan(t, seed)

	var post Step
	for _, s := range plan.Steps {
		if s.Verb == "POST" {
			post = s
		}
		if s.Verb == "GET" && len(s.Body) > 0 {
			t.Errorf("a GET was given a body: %s", s.Body)
		}
	}
	if !strings.Contains(string(post.Body), "cola") {
		t.Errorf("POST /orders did not get its seed body: %q", post.Body)
	}
}

// The checklist is the point of the exercise: a store nothing wrote to and a
// unit nobody invoked are both green deploys and broken applications.
func TestEveryRuntimeEdgeBecomesAnExpectation(t *testing.T) {
	plan := testPlan(t, Seed{})

	want := map[string]string{
		"expose/nomnom-api -exposes-> execution_unit/storefront":    "http",
		"execution_unit/storefront -uses-> persist_kv/orders":       "store",
		"execution_unit/storefront -calls-> execution_unit/pricing": "invocations",
		"execution_unit/notify -subscribes-> pubsub/orderPlaced":    "invocations",
		"execution_unit/notify -uses-> persist_fs/notifications":    "bucket",
	}
	got := map[string]string{}
	for _, e := range plan.Expect {
		got[e.Edge] = e.Kind
	}

	for edge, kind := range want {
		if got[edge] != kind {
			t.Errorf("edge %q: kind %q, want %q", edge, got[edge], kind)
		}
	}
	// Nothing from the resource layer, and nothing that is a build-time fact:
	// a role is not something traffic flows through.
	for edge := range got {
		if strings.Contains(edge, "aws.") || strings.Contains(edge, "resolves_to") {
			t.Errorf("%q is not a runtime edge and should not be on the checklist", edge)
		}
	}
	if len(got) != len(want) {
		t.Errorf("checklist has %d entries, want %d: %v", len(got), len(want), got)
	}
}

// Every expectation has to say what a failure would mean. A checklist that
// reports "execution_unit/notify -subscribes-> pubsub/orderPlaced: absent" and
// nothing else sends the reader to the wrong place.
func TestEveryExpectationExplainsItself(t *testing.T) {
	plan := testPlan(t, Seed{})
	for _, e := range plan.Expect {
		if len(e.Why) < 40 {
			t.Errorf("expectation %q has no useful explanation: %q", e.Edge, e.Why)
		}
		if e.Target == "" {
			t.Errorf("expectation %q names nothing to look at", e.Edge)
		}
	}
}

func TestAnApplicationWithNoGatewayIsRefused(t *testing.T) {
	p := &Program{Intents: []Intent{
		{Key: Key{Kind: "execution_unit", ID: "worker"}, Payload: json.RawMessage(`{"id":"worker"}`)},
	}}
	if _, err := DerivePlan("headless", p, Seed{}); err == nil {
		t.Fatal("expected an error: there is nothing to put load on")
	} else if !strings.Contains(err.Error(), "exposes nothing") {
		t.Errorf("error = %v", err)
	}
}

func TestPathParametersAreFound(t *testing.T) {
	for path, want := range map[string]string{
		"/orders/{order_id}": "order_id",
		"/pets/{pet_id}":     "pet_id",
		"/health":            "",
		"/orders":            "",
	} {
		if got := paramOf(path); got != want {
			t.Errorf("paramOf(%q) = %q, want %q", path, got, want)
		}
	}
}

// A unit nothing invokes cannot be exercised by a load test that speaks HTTP,
// however hard it tries. That is a limit of the harness rather than a defect
// in the application, and the checklist has to be able to tell them apart --
// examples/mixed has a worker whose function nothing calls.
func TestReachabilityFollowsCallsAndMessages(t *testing.T) {
	var p Program
	if err := json.Unmarshal([]byte(irJSON), &p); err != nil {
		t.Fatal(err)
	}
	// storefront is the gateway's unit; pricing is reached by a call. notify
	// subscribes to orderPlaced, which nothing in this fixture publishes to,
	// so it is not reachable.
	reached := reachableUnits(&p, "storefront")
	for _, want := range []string{"storefront", "pricing"} {
		if !reached[want] {
			t.Errorf("%s should be reachable: %v", want, reached)
		}
	}
	if reached["notify"] {
		t.Errorf("notify subscribes to a topic nothing publishes to; it is not reachable: %v", reached)
	}
}

func TestPublishingMakesASubscriberReachable(t *testing.T) {
	var p Program
	if err := json.Unmarshal([]byte(irJSON), &p); err != nil {
		t.Fatal(err)
	}
	p.Edges = append(p.Edges, Edge{
		From: Key{Kind: "execution_unit", ID: "storefront"},
		To:   Key{Kind: "pubsub", ID: "orderPlaced"},
		Kind: "publishes",
	})
	reached := reachableUnits(&p, "storefront")
	if !reached["notify"] {
		t.Errorf("a subscriber of a published topic is reachable: %v", reached)
	}
}

// Which expectations carry the flag matters as much as the flag: an edge whose
// source is unreachable must say so, and an edge whose source is reachable
// must not get the excuse.
func TestExpectationsRecordReachability(t *testing.T) {
	plan := testPlan(t, Seed{})
	byEdge := map[string]Evidence{}
	for _, e := range plan.Expect {
		byEdge[e.Edge] = e
	}
	if !byEdge["execution_unit/storefront -uses-> persist_kv/orders"].SourceReachable {
		t.Error("the gateway's own unit is reachable")
	}
	if byEdge["execution_unit/notify -uses-> persist_fs/notifications"].SourceReachable {
		t.Error("notify is not reachable in this fixture and should not be marked so")
	}
}
