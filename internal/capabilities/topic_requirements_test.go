package capabilities

import (
	"strings"
	"testing"

	"github.com/spf13/afero"

	"github.com/cloudcompiler/cloudcc/internal/compiler"
	"github.com/cloudcompiler/cloudcc/internal/config"
	"github.com/cloudcompiler/cloudcc/internal/ir"
)

// compileSource runs the intent stages over one file, so a program with
// diagnostics still produces a context to assert against.
func compileSource(t *testing.T, files map[string]string) *compiler.Context {
	t.Helper()
	return compileSourceWithConfig(t, files, "")
}

func compileSourceWithConfig(t *testing.T, files map[string]string, yaml string) *compiler.Context {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		write(t, root, name, content)
	}
	cfg := config.New()
	cfg.App = "test"
	if yaml != "" {
		write(t, root, "cloudcc.yaml", yaml)
		loaded, err := config.Load(root + "/cloudcc.yaml")
		if err != nil {
			t.Fatal(err)
		}
		cfg = loaded
	}
	ctx := compiler.NewContext(cfg, root, afero.NewMemMapFs())
	t.Cleanup(func() { ctx.Files.Close() })
	c, err := compiler.NewCompiler(IntentChain())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Compile(ctx); err != nil {
		t.Fatalf("compile returned a hard error: %v", err)
	}
	return ctx
}

// The path from a Topic()'s arguments to a chosen backing, through detection
// and the capability plugin. The selector's own table is tested next door; what
// these check is that the arguments survive the trip, which is where a feature
// like this actually breaks.

func topicIn(t *testing.T, src string) (*ir.Topic, []string) {
	t.Helper()
	ctx := compileSource(t, map[string]string{
		"app.py": "import cloudcompiler as cloudcc\n" + src,
	})
	var messages []string
	for _, item := range ctx.Diags.Items() {
		messages = append(messages, item.Message)
	}
	intents := ctx.Graph.IntentsOfKind(config.KindPubSub)
	if len(intents) == 0 {
		return nil, messages
	}
	return intents[0].(*ir.Topic), messages
}

func TestABareTopicKeepsTheDefaults(t *testing.T) {
	topic, msgs := topicIn(t, `events = cloudcc.persist(cloudcc.Topic(), id="events")`)
	if topic == nil {
		t.Fatalf("no topic intent: %v", msgs)
	}
	if topic.Config().Type != "sns" {
		t.Errorf("a topic with no stated requirements resolved to %q, want sns",
			topic.Config().Type)
	}
	if topic.Requires.Ordering != "none" || topic.Requires.Subscribers != "many" {
		t.Errorf("defaults not applied: %+v", topic.Requires)
	}
}

// The arguments have to reach the selector. A plugin that read the hint but
// dropped the client's arguments would compile every topic to SNS and look
// entirely correct from the outside.
func TestTheDeclaredRequirementsReachTheChoice(t *testing.T) {
	topic, msgs := topicIn(t, `events = cloudcc.persist(
    cloudcc.Topic(subscribers="one", ordering="key", delivery="exactly_once"),
    id="refunds",
)`)
	if topic == nil {
		t.Fatalf("no topic intent: %v", msgs)
	}
	if topic.Requires.Subscribers != "one" || topic.Requires.Ordering != "key" {
		t.Fatalf("requirements were not read: %+v", topic.Requires)
	}
	if topic.Config().Type != "sqs_fifo" {
		t.Errorf("resolved to %q, want sqs_fifo", topic.Config().Type)
	}
	if topic.Because == "" {
		t.Error("the plan should say which requirement forced the service")
	}
}

func TestReplayAsksForAStream(t *testing.T) {
	topic, msgs := topicIn(t,
		`events = cloudcc.persist(cloudcc.Topic(replay=True), id="audit")`)
	if topic == nil {
		t.Fatalf("no topic intent: %v", msgs)
	}
	if !topic.Requires.Replay {
		t.Fatalf("replay was not read: %+v", topic.Requires)
	}
	if topic.Config().Type != "kinesis" {
		t.Errorf("resolved to %q, want kinesis", topic.Config().Type)
	}
}

// An unsatisfiable set stops the compile, naming the constraint. Silently
// picking the nearest service is the failure this capability exists to prevent.
func TestAnImpossibleTopicIsRefused(t *testing.T) {
	_, msgs := topicIn(t, `events = cloudcc.persist(
    cloudcc.Topic(ordering="total", replay=True), id="events")`)
	joined := strings.Join(msgs, "\n")
	if !strings.Contains(joined, "single-shard") {
		t.Errorf("expected an error naming the constraint to relax:\n%s", joined)
	}
}

// A requirement that is not a literal cannot be read without running the
// program, which is the same rule as `id=name`.
func TestANonLiteralRequirementIsRefused(t *testing.T) {
	_, msgs := topicIn(t, `mode = "one"
events = cloudcc.persist(cloudcc.Topic(subscribers=mode), id="events")`)
	joined := strings.Join(msgs, "\n")
	if !strings.Contains(joined, "must be a literal") {
		t.Errorf("expected a literal-argument error:\n%s", joined)
	}
}

// cloudcc.yaml is checked against the requirements rather than obeyed.
func TestAConfiguredTypeThatCannotMeetTheRequirementsIsRefused(t *testing.T) {
	ctx := compileSourceWithConfig(t, map[string]string{
		"app.py": "import cloudcompiler as cloudcc\n" +
			`events = cloudcc.persist(cloudcc.Topic(ordering="total"), id="events")` + "\n",
	}, "app: demo\nprovider: aws\npersisted:\n  events:\n    type: sns\n")

	var joined string
	for _, item := range ctx.Diags.Items() {
		joined += item.Message + "\n"
	}
	if !strings.Contains(joined, "sns_fifo") {
		t.Errorf("expected the error to name the service the code asked for:\n%s", joined)
	}
}
