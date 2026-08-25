package topology

import (
	"strings"
	"testing"

	"github.com/cloudcompiler/cloudcc/internal/config"
	"github.com/cloudcompiler/cloudcc/internal/ir"
)

// The architecture diagram is the picture to review before a deploy: every
// resource that will exist in the account. Its whole value is that it is
// rendered from the program's own edges, so it cannot show something that was
// not compiled or omit something that was.
//
// These check exactly that property, because a diagram that quietly drops a
// resource is worse than no diagram -- a reviewer would sign off on it.

func archProgram(t *testing.T) *ir.Program {
	t.Helper()
	p := ir.NewProgram()

	unit := &ir.ExecUnit{Entrypoints: []string{"app.py"}}
	unit.ID = "main"
	if err := unit.Configure(config.ResourceConfig{Type: "lambda"}); err != nil {
		t.Fatal(err)
	}
	p.AddIntent(unit)

	store := &ir.Persist{Kind: config.KindPersistKV}
	store.ID = "pets"
	if err := store.Configure(config.ResourceConfig{Type: "dynamodb"}); err != nil {
		t.Fatal(err)
	}
	p.AddIntent(store)
	p.Connect(unit.Key(), store.Key(), ir.EdgeUses)

	// Two resources per intent, as a real expansion produces: the thing itself
	// and the plumbing it needs.
	fn := ir.NewResource("aws.lambda", "main", "aws.lambda.Function", nil, nil)
	role := ir.NewResource("aws.iam.role", "main", "aws.iam.Role", nil, nil)
	table := ir.NewResource("aws.dynamodb", "pets", "aws.dynamodb.Table", nil, nil)
	p.Resolve(unit.Key(), fn)
	p.Resolve(unit.Key(), role)
	p.Resolve(store.Key(), table)
	p.Connect(fn.Key(), role.Key(), ir.EdgeDependsOn)
	p.Connect(fn.Key(), table.Key(), ir.EdgeUses)

	return p
}

func TestTheArchitectureDiagramShowsEveryResolvedResource(t *testing.T) {
	p := archProgram(t)
	got := string(Mermaid(p, Options{App: "demo", View: Resources}))

	for _, want := range []string{
		"aws_lambda_main", "aws_iam_role_main", "aws_dynamodb_pets",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the diagram omits %s, which will exist in the account:\n%s", want, got)
		}
	}
	// And nothing from the other layer, which is drawn separately.
	for _, unwanted := range []string{"execution_unit_main", "persist_kv_pets"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("the architecture diagram should not carry intent nodes (%s):\n%s",
				unwanted, got)
		}
	}
}

func TestTheTopologyDiagramStillShowsOnlyIntents(t *testing.T) {
	p := archProgram(t)
	got := string(Mermaid(p, Options{App: "demo", View: Intents}))

	if !strings.Contains(got, "execution_unit_main") || !strings.Contains(got, "persist_kv_pets") {
		t.Errorf("the topology diagram lost an intent:\n%s", got)
	}
	if strings.Contains(got, "aws_iam_role_main") {
		t.Errorf("a role is not something the program declared:\n%s", got)
	}
}

// Reading "aws.apigatewayv2.integration" a dozen times is how a diagram stops
// being worth looking at.
func TestResourceLabelsDropTheProviderPrefix(t *testing.T) {
	p := archProgram(t)
	got := string(Mermaid(p, Options{App: "demo", View: Resources}))

	if !strings.Contains(got, `"pets\ndynamodb"`) {
		t.Errorf("expected a short service label:\n%s", got)
	}
	if strings.Contains(got, "aws.dynamodb\"") {
		t.Errorf("the provider prefix survived into a label:\n%s", got)
	}
}

// Both diagrams use one visual vocabulary, so the second reads as an expansion
// of the first rather than as an unrelated picture.
func TestBothLayersShareTheirShapes(t *testing.T) {
	p := archProgram(t)
	intents := string(Mermaid(p, Options{App: "demo", View: Intents}))
	arch := string(Mermaid(p, Options{App: "demo", View: Resources}))

	// A store is a cylinder in both.
	if !strings.Contains(intents, "persist_kv_pets[(") {
		t.Errorf("a declared store should be a cylinder:\n%s", intents)
	}
	if !strings.Contains(arch, "aws_dynamodb_pets[(") {
		t.Errorf("a DynamoDB table should be a cylinder too:\n%s", arch)
	}
	// Supporting cast -- roles, log groups -- is a plain box.
	if !strings.Contains(arch, "aws_iam_role_main[\"") {
		t.Errorf("a role should be a plain box:\n%s", arch)
	}
}

// The two views must never write to the same files, or a compile would leave
// only whichever ran last.
func TestTheTwoViewsWriteDifferentFiles(t *testing.T) {
	tm, td := Intents.Files()
	am, ad := Resources.Files()
	if tm == am || td == ad {
		t.Fatalf("the views share a filename: %s/%s and %s/%s", tm, td, am, ad)
	}
	if PNGFile("demo") == ArchPNGFile("demo") {
		t.Error("the two PNGs would overwrite each other")
	}
}
