package topology

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudcompiler/cc/internal/config"
	"github.com/cloudcompiler/cc/internal/ir"
	"github.com/spf13/afero"
)

// program builds a small two-unit program: one exposed, one subscribing, both
// sharing a store.
func program(t *testing.T) *ir.Program {
	t.Helper()
	p := ir.NewProgram()

	api := &ir.ExecUnit{}
	api.ID = "api"
	worker := &ir.ExecUnit{}
	worker.ID = "worker"
	store := &ir.Persist{Kind: config.KindPersistKV}
	store.ID = "petsByOwner"
	topic := &ir.Topic{}
	topic.ID = "petEvents"
	gw := &ir.Expose{Unit: "api"}
	gw.ID = "pet-api"

	for _, in := range []ir.Intent{api, worker, store, topic, gw} {
		p.AddIntent(in)
	}
	p.Connect(gw.Key(), api.Key(), ir.EdgeExposes)
	p.Connect(api.Key(), store.Key(), ir.EdgeUses)
	p.Connect(worker.Key(), store.Key(), ir.EdgeUses)
	p.Connect(api.Key(), topic.Key(), ir.EdgePublishes)
	p.Connect(worker.Key(), topic.Key(), ir.EdgeSubscribes)

	// A resolved resource, to prove the resolves_to edges stay out of the
	// intent view.
	table := ir.NewResource("aws.dynamodb", "petsByOwner", "aws.dynamodb.Table", nil, nil)
	p.Resolve(store.Key(), table)
	return p
}

func TestMermaidDrawsEveryIntentAndEdge(t *testing.T) {
	got := string(Mermaid(program(t), Options{App: "petstore"}))

	for _, want := range []string{
		"flowchart LR",
		`execution_unit_api["api`,
		`persist_kv_petsByOwner[("petsByOwner`,
		"expose_pet_api -->|exposes| execution_unit_api",
		"execution_unit_api -->|uses| persist_kv_petsByOwner",
		"execution_unit_worker -->|uses| persist_kv_petsByOwner",
		"execution_unit_api -->|publishes| pubsub_petEvents",
		"execution_unit_worker -.->|subscribes| pubsub_petEvents",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestIntentViewExcludesResolvedResources(t *testing.T) {
	got := string(Mermaid(program(t), Options{App: "petstore"}))
	if strings.Contains(got, "aws_dynamodb") {
		t.Errorf("the intent view should not draw concrete resources:\n%s", got)
	}
	if strings.Contains(got, "resolves to") {
		t.Errorf("resolves_to edges would double every node's connections:\n%s", got)
	}
}

func TestResourceViewDrawsConcreteResources(t *testing.T) {
	got := string(Mermaid(program(t), Options{App: "petstore", View: Resources}))
	if !strings.Contains(got, "aws_dynamodb_petsByOwner") {
		t.Errorf("the resource view should draw the table:\n%s", got)
	}
	if strings.Contains(got, "execution_unit_api[") {
		t.Errorf("the resource view should not draw intents:\n%s", got)
	}
}

func TestDOTIsValidGraphviz(t *testing.T) {
	dot := DOT(program(t), Options{App: "petstore"})
	if !strings.HasPrefix(string(dot), "// Architecture of petstore") {
		t.Errorf("missing header:\n%s", dot)
	}

	bin, err := exec.LookPath("dot")
	if err != nil {
		t.Skip("graphviz is not installed; the DOT output was not validated")
	}
	cmd := exec.Command(bin, "-Tsvg", "-o", filepath.Join(t.TempDir(), "out.svg"))
	cmd.Stdin = strings.NewReader(string(dot))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("graphviz rejected the generated DOT: %v\n%s\n--- dot ---\n%s", err, out, dot)
	}
}

func TestRenderingIsDeterministic(t *testing.T) {
	first := string(Mermaid(program(t), Options{App: "petstore"}))
	for i := 0; i < 20; i++ {
		if got := string(Mermaid(program(t), Options{App: "petstore"})); got != first {
			t.Fatalf("run %d differs:\n%s\n---\n%s", i, first, got)
		}
	}
}

func TestWriteAlwaysProducesTextFormats(t *testing.T) {
	fs := afero.NewMemMapFs()
	result, err := Write(program(t), fs, "", Options{App: "petstore"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{MermaidFile, DOTFile} {
		if ok, _ := afero.Exists(fs, name); !ok {
			t.Errorf("%s was not written", name)
		}
	}
	if result.Notice == "" {
		t.Error("an in-memory output should explain why no PNG was rendered")
	}
}

func TestWriteRendersAPNGWhenGraphvizIsPresent(t *testing.T) {
	if _, err := exec.LookPath("dot"); err != nil {
		t.Skip("graphviz is not installed")
	}
	dir := t.TempDir()
	fs := afero.NewBasePathFs(afero.NewOsFs(), dir)

	result, err := Write(program(t), fs, dir, Options{App: "petstore"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Notice != "" {
		t.Errorf("unexpected notice with graphviz installed: %s", result.Notice)
	}
	png := filepath.Join(dir, PNGFile("petstore"))
	info, err := os.Stat(png)
	if err != nil {
		t.Fatalf("no PNG written: %v", err)
	}
	if info.Size() == 0 {
		t.Error("the rendered PNG is empty")
	}
}

// TestMissingGraphvizIsANoticeNotAFailure pins the graceful degradation: a
// compile must not depend on an optional tool.
func TestMissingGraphvizIsANoticeNotAFailure(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // an empty PATH: nothing is installed
	dir := t.TempDir()
	fs := afero.NewBasePathFs(afero.NewOsFs(), dir)

	result, err := Write(program(t), fs, dir, Options{App: "petstore"})
	if err != nil {
		t.Fatalf("a missing optional tool must not fail the render: %v", err)
	}
	if !strings.Contains(result.Notice, "graphviz") {
		t.Errorf("the notice should name what is missing and how to get it: %q", result.Notice)
	}
	if ok, _ := afero.Exists(fs, MermaidFile); !ok {
		t.Error("the text formats must still be written")
	}
}

func TestNodeIDsAreSafeIdentifiers(t *testing.T) {
	for _, key := range []ir.Key{
		{Kind: "persist_kv", ID: "pets/by owner"},
		{Kind: "static_unit", ID: "site.v2"},
		{Kind: "expose", ID: "pet-api"},
	} {
		got := nodeID(key)
		for i, r := range got {
			ok := r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
			if !ok {
				t.Errorf("nodeID(%v) = %q has an unsafe character at %d", key, got, i)
			}
		}
	}
}
