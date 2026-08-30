package aws

import (
	"strings"
	"testing"

	"github.com/cloudcompiler/cloudcc/internal/config"
	"github.com/cloudcompiler/cloudcc/internal/ir"
)

// The selector decides SNS or SQS from a program's requirements; these tests
// are about what the resolver then builds, which is a different question and
// where the two backings stop resembling each other. SNS pushes: a subscription
// plus a resource policy on the function. SQS is pulled: an event source
// mapping plus permissions on the function's own role.

// topicProgram builds a program with one topic and one Lambda unit that
// publishes to it and subscribes to it, resolved through the real resolver.
func topicProgram(t *testing.T, backing string, unitTimeout int) *ir.Program {
	t.Helper()

	p := ir.NewProgram()

	unit := &ir.ExecUnit{Entrypoints: []string{"app.py"}, Runtime: "python3.12", Handler: "index.handler"}
	unit.ID = "worker"
	if err := unit.Configure(config.ResourceConfig{Type: config.TypeFunction, Timeout: unitTimeout}); err != nil {
		t.Fatal(err)
	}
	p.AddIntent(unit)

	topic := &ir.Topic{Requires: ir.TopicRequires{Subscribers: "one", Ordering: "none", Delivery: "at_least_once"}}
	topic.ID = "events"
	if err := topic.Configure(config.ResourceConfig{Type: backing}); err != nil {
		t.Fatal(err)
	}
	p.AddIntent(topic)

	p.Connect(unit.Key(), topic.Key(), ir.EdgeUses)
	p.Connect(unit.Key(), topic.Key(), ir.EdgePublishes)
	p.Connect(unit.Key(), topic.Key(), ir.EdgeSubscribes)

	r := &Resolver{App: "test", Program: p, Config: config.New()}
	if err := r.Resolve(); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return p
}

func resource(t *testing.T, p *ir.Program, kind, id string) ir.Resource {
	t.Helper()
	res, ok := p.Resource(ir.Key{Kind: kind, ID: id})
	if !ok {
		var have []string
		for _, r := range p.Resources() {
			have = append(have, r.Key().String())
		}
		t.Fatalf("no %s resource %q; the program has %s", kind, id, strings.Join(have, ", "))
	}
	return res
}

func noResource(t *testing.T, p *ir.Program, kind, id string) {
	t.Helper()
	if _, ok := p.Resource(ir.Key{Kind: kind, ID: id}); ok {
		t.Fatalf("%s %q should not exist", kind, id)
	}
}

func TestAQueueBackedTopicIsConsumedThroughAnEventSourceMapping(t *testing.T) {
	p := topicProgram(t, TopicSQS, 0)

	resource(t, p, KindSQSQueue, "events")
	// The push-side wiring must be absent. A subscription and a mapping both
	// deliver, and a topic with both delivers twice.
	noResource(t, p, KindSNSSub, "worker-events")

	esm := resource(t, p, KindLambdaESM, "worker-events")
	if got := esm.Props()["eventSourceArn"]; got != (ir.Ref{Key: ir.Key{Kind: KindSQSQueue, ID: "events"}, Prop: "arn"}) {
		t.Errorf("the mapping polls %v, not the queue", got)
	}
	// Created after the role policy, and by an option Pulumi acts on rather
	// than only by an edge that orders the generated source. Lambda validates
	// the role when the mapping is created, so a mapping that races its policy
	// fails on some deploys and not others.
	dep, ok := esm.Opts()["dependsOn"].([]any)
	if !ok || len(dep) != 1 || dep[0] != (ir.Ref{Key: ir.Key{Kind: KindLambdaPolicy, ID: "worker"}}) {
		t.Errorf("the mapping does not depend on the unit's role policy: %v", esm.Opts())
	}
}

func TestAQueueSubscriberMayReadAndDeleteAndAPublisherOnlySends(t *testing.T) {
	p := topicProgram(t, TopicSQS, 0)
	policy := resource(t, p, KindLambdaPolicy, "worker")

	granted := map[string]bool{}
	doc, ok := policy.Props()["policy"].(ir.JSONDoc)
	if !ok {
		t.Fatalf("the policy document is %T", policy.Props()["policy"])
	}
	for _, stmt := range doc.Value.(map[string]any)["Statement"].([]any) {
		for _, action := range stmt.(map[string]any)["Action"].([]any) {
			granted[action.(string)] = true
		}
	}

	for _, want := range []string{
		"sqs:SendMessage",        // it publishes
		"sqs:ReceiveMessage",     // and consumes
		"sqs:DeleteMessage",      // which means removing what it handled
		"sqs:GetQueueAttributes", // which the Lambda poller itself calls
	} {
		if !granted[want] {
			t.Errorf("the unit's role does not grant %s; the mapping would exist and read nothing", want)
		}
	}
	if granted["sns:Publish"] {
		t.Error("a queue-backed topic granted an SNS action")
	}
}

// The visibility timeout is derived from the subscriber, not defaulted. AWS
// refuses a mapping whose function can outrun its queue's visibility timeout,
// and the subscribers are in the graph before the queue is built -- so this is
// knowable at compile time rather than eight minutes into a deploy.
func TestAQueueIsInvisibleForAtLeastAsLongAsItsSubscriberCanRun(t *testing.T) {
	for _, tc := range []struct{ timeout, want int }{
		{0, 30},    // unset: the AWS default for both
		{30, 30},   // equal is legal
		{120, 120}, // a slow subscriber drags the queue with it
	} {
		p := topicProgram(t, TopicSQS, tc.timeout)
		queue := resource(t, p, KindSQSQueue, "events")
		if got := queue.Props()["visibilityTimeoutSeconds"]; got != tc.want {
			t.Errorf("subscriber timeout %ds gave a visibility timeout of %v, want %d",
				tc.timeout, got, tc.want)
		}
	}
}

// The SNS path is unchanged, which is the point: adding a second backing must
// not quietly alter the first.
func TestATopicBackedTopicKeepsItsSubscriptionAndItsResourcePolicy(t *testing.T) {
	p := topicProgram(t, TopicSNS, 0)

	resource(t, p, KindSNSTopic, "events")
	resource(t, p, KindSNSSub, "worker-events")
	resource(t, p, KindLambdaPerm, "worker-events-sns")
	noResource(t, p, KindLambdaESM, "worker-events")

	// A pushed-to subscriber needs nothing on its role. Granting queue actions
	// against a topic would be permissions for a service that is not there.
	policy := resource(t, p, KindLambdaPolicy, "worker")
	doc := policy.Props()["policy"].(ir.JSONDoc)
	published := false
	for _, stmt := range doc.Value.(map[string]any)["Statement"].([]any) {
		for _, action := range stmt.(map[string]any)["Action"].([]any) {
			if strings.HasPrefix(action.(string), "sqs:") {
				t.Errorf("an SNS-backed topic granted %s", action)
			}
			if action == "sns:Publish" {
				published = true
			}
		}
	}
	if !published {
		t.Error("the unit publishes to this topic and was not granted sns:Publish")
	}
}

// Both spellings of the address, plus the name of the service, because the shim
// cannot tell which client to build from an ARN alone.
func TestATopicPublishesTheBackingItResolvedTo(t *testing.T) {
	for backing, kind := range map[string]string{TopicSNS: KindSNSTopic, TopicSQS: KindSQSQueue} {
		p := topicProgram(t, backing, 0)
		env := resource(t, p, kind, "events").EnvOutputs()
		if got := env[EnvTopicBacking("events")].Expr; got != backing {
			t.Errorf("%s: the backing binding is %v", backing, got)
		}
		if _, ok := env[EnvTopicARN("events")]; !ok {
			t.Errorf("%s: no ARN binding", backing)
		}
		_, hasURL := env[EnvTopicURL("events")]
		if want := backing == TopicSQS; hasURL != want {
			// A URL is how a queue is addressed and is not a thing a topic has.
			t.Errorf("%s: URL binding present=%v, want %v", backing, hasURL, want)
		}
	}
}

// A container that subscribes to a topic is refused, because nothing would
// deliver to it.
//
// This produced no subscription, no event source mapping and no permission,
// and the handler was simply never called -- messages that vanish, with a
// comment in the resolver claiming a non-Lambda unit "subscribes through its
// own runtime". Nothing did.
func TestAContainerCannotSubscribe(t *testing.T) {
	p := ir.NewProgram()

	unit := &ir.ExecUnit{Entrypoints: []string{"worker.py"}, Runtime: "python3.12"}
	unit.ID = "worker"
	if err := unit.Configure(config.ResourceConfig{Type: config.TypeContainer}); err != nil {
		t.Fatal(err)
	}
	p.AddIntent(unit)

	topic := &ir.Topic{Requires: ir.TopicRequires{Subscribers: "many", Ordering: "none", Delivery: "at_least_once"}}
	topic.ID = "events"
	if err := topic.Configure(config.ResourceConfig{Type: TopicSNS}); err != nil {
		t.Fatal(err)
	}
	p.AddIntent(topic)
	p.Connect(unit.Key(), topic.Key(), ir.EdgeUses)
	p.Connect(unit.Key(), topic.Key(), ir.EdgeSubscribes)

	err := (&Resolver{App: "test", Program: p, Config: config.New()}).Resolve()
	if err == nil {
		t.Fatal("nothing delivers to a container, so this must not resolve")
	}
	for _, want := range []string{"worker", "container", "not yet supported"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message should mention %q:\n%s", want, err)
		}
	}
}

// A container that only *uses* a topic -- holding the handle because it
// bundles the module that declares it -- is fine. Only subscribing is refused.
func TestAContainerMayHoldATopicItDoesNotSubscribeTo(t *testing.T) {
	p := ir.NewProgram()

	unit := &ir.ExecUnit{Entrypoints: []string{"worker.py"}, Runtime: "python3.12"}
	unit.ID = "worker"
	if err := unit.Configure(config.ResourceConfig{Type: config.TypeContainer}); err != nil {
		t.Fatal(err)
	}
	p.AddIntent(unit)

	topic := &ir.Topic{Requires: ir.TopicRequires{Subscribers: "many", Ordering: "none", Delivery: "at_least_once"}}
	topic.ID = "events"
	if err := topic.Configure(config.ResourceConfig{Type: TopicSNS}); err != nil {
		t.Fatal(err)
	}
	p.AddIntent(topic)
	p.Connect(unit.Key(), topic.Key(), ir.EdgeUses)
	p.Connect(unit.Key(), topic.Key(), ir.EdgePublishes)

	if err := (&Resolver{App: "test", Program: p, Config: config.New()}).Resolve(); err != nil {
		t.Fatalf("publishing from a container is an ordinary API call: %v", err)
	}
}
