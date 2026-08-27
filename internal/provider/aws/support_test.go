package aws

import (
	"reflect"
	"testing"

	"github.com/cloudcompiler/cloudcc/internal/config"
)

func TestSupportLevels(t *testing.T) {
	cases := []struct {
		kind, typ string
		want      Level
	}{
		{config.KindExecutionUnit, config.TypeFunction, Supported},
		{config.KindExecutionUnit, config.TypeContainer, Supported},
		// Accepted by the schema, rejected at compile time rather than
		// silently becoming something else.
		{config.KindPersistORM, "cockroachdb_serverless", NotYetSupported},
		{config.KindExecutionUnit, "nomad", Unknown},
		{config.KindPersistORM, "rds_postgres", Supported},
		{config.KindPersistORM, "cockroachdb_serverless", NotYetSupported},
		{config.KindPersistKV, "mongodb", Unknown},
		{config.KindExpose, "apigateway", Supported},
		{config.KindExpose, "alb", Supported},
		// Config values select nothing, so anything is fine.
		{config.KindConfig, "", Supported},
		{"not_a_capability", "anything", Unknown},
	}
	for _, c := range cases {
		if got := Support(c.kind, c.typ); got != c.want {
			t.Errorf("Support(%q, %q) = %v, want %v", c.kind, c.typ, got, c.want)
		}
	}
}

func TestEveryCapabilityKindHasSupportEntries(t *testing.T) {
	for _, kind := range config.Kinds {
		if _, ok := typeSupport[kind]; !ok {
			t.Errorf("capability %q has no entry in the support table", kind)
		}
	}
}

func TestSupportedTypesExcludeTheUnimplemented(t *testing.T) {
	// Both compute types are built. Kubernetes is not a third type -- it is a
	// `platform:` on the container one, which is a different axis and does not
	// belong in this table.
	if got := SupportedTypes(config.KindExecutionUnit); !reflect.DeepEqual(got,
		[]string{config.TypeContainer, config.TypeFunction}) {
		t.Errorf("SupportedTypes = %v", got)
	}

	// A kind that does have something planned but not built, so the
	// distinction the table draws is still exercised.
	if got := SupportedTypes(config.KindPubSub); reflect.DeepEqual(got, AllTypes(config.KindPubSub)) {
		t.Errorf("pubsub lists the same types as supported and as all: %v", got)
	}
	all := AllTypes(config.KindPubSub)
	found := false
	for _, t := range all {
		if t == "sqs" {
			found = true
		}
	}
	if !found {
		t.Errorf("AllTypes should include what is planned but not built: %v", all)
	}
}

// TestNeedsVPC pins the rule that Lambda does not pull a VPC in. Putting a
// function in a VPC costs cold-start time and buys nothing unless it has to
// reach a private resource.
func TestNeedsVPC(t *testing.T) {
	cases := []struct {
		types []string
		want  bool
	}{
		{[]string{config.TypeFunction, "apigateway", "dynamodb", "s3", "sns"}, false},
		{[]string{config.TypeFunction, config.TypeContainer}, true},
		{[]string{config.TypeFunction, "rds_postgres"}, true},
		{[]string{config.TypeFunction, "elasticache"}, true},
		{[]string{config.TypeFunction, "memorydb"}, true},
		{[]string{config.TypeFunction, "alb"}, true},
		{nil, false},
	}
	for _, c := range cases {
		if got := NeedsVPC(c.types); got != c.want {
			t.Errorf("NeedsVPC(%v) = %v, want %v", c.types, got, c.want)
		}
	}
}
