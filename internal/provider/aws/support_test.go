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
		{config.KindExecutionUnit, "lambda", Supported},
		{config.KindExecutionUnit, "ecs", Supported},
		// Accepted by the schema, rejected at compile time rather than
		// silently becoming something else.
		{config.KindExecutionUnit, "eks", NotYetSupported},
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
	got := SupportedTypes(config.KindExecutionUnit)
	if !reflect.DeepEqual(got, []string{"ecs", "lambda"}) {
		t.Errorf("SupportedTypes = %v", got)
	}
	all := AllTypes(config.KindExecutionUnit)
	if !reflect.DeepEqual(all, []string{"ecs", "eks", "lambda"}) {
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
		{[]string{"lambda", "apigateway", "dynamodb", "s3", "sns"}, false},
		{[]string{"lambda", "ecs"}, true},
		{[]string{"lambda", "rds_postgres"}, true},
		{[]string{"lambda", "elasticache"}, true},
		{[]string{"lambda", "memorydb"}, true},
		{[]string{"lambda", "alb"}, true},
		{nil, false},
	}
	for _, c := range cases {
		if got := NeedsVPC(c.types); got != c.want {
			t.Errorf("NeedsVPC(%v) = %v, want %v", c.types, got, c.want)
		}
	}
}
