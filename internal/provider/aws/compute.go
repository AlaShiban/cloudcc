package aws

import (
	"fmt"
	"sort"

	"github.com/cloudcompiler/cloudcc/internal/config"
	"github.com/cloudcompiler/cloudcc/internal/ir"
	"github.com/cloudcompiler/cloudcc/internal/sanitize"
)

// execUnit expands one execution unit into compute plus the IAM role and log
// group it needs.
func (r *Resolver) execUnit(u *ir.ExecUnit) error {
	switch u.Config().Type {
	case "lambda":
		return r.lambda(u)
	case "ecs":
		return r.ecsServiceUnit(u)
	}
	return fmt.Errorf("no AWS mapping for execution unit type %q", u.Config().Type)
}

func (r *Resolver) lambda(u *ir.ExecUnit) error {
	fnName := r.uniqueName("lambda", u.ID, sanitize.LambdaFunction)
	roleKey := ir.Key{Kind: KindLambdaRole, ID: u.ID}
	logsKey := ir.Key{Kind: KindLogGroup, ID: u.ID}
	fnKey := ir.Key{Kind: KindLambda, ID: u.ID}

	role := ir.NewResource(KindLambdaRole, u.ID, "aws.iam.Role", map[string]any{
		"name": sanitize.IAMName(r.App, u.ID, "role"),
		"assumeRolePolicy": ir.JSONDoc{Value: map[string]any{
			"Version": "2012-10-17",
			"Statement": []any{map[string]any{
				"Effect":    "Allow",
				"Action":    "sts:AssumeRole",
				"Principal": map[string]any{"Service": "lambda.amazonaws.com"},
			}},
		}},
		"managedPolicyArns": []any{
			"arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole",
		},
	}, nil)
	r.Program.Resolve(u.Key(), role)

	// A log group created up front, rather than implicitly by the first
	// invocation, is what makes retention configurable and destroy clean.
	logs := ir.NewResource(KindLogGroup, u.ID, "aws.cloudwatch.LogGroup", map[string]any{
		"name":            "/aws/lambda/" + fnName,
		"retentionInDays": 14,
	}, nil)
	r.Program.Resolve(u.Key(), logs)

	if policy := r.unitPolicy(u, roleKey); policy != nil {
		r.Program.Resolve(u.Key(), policy)
		r.Program.Connect(policy.Key(), roleKey, ir.EdgeDependsOn)
	}

	fn := ir.NewResource(KindLambda, u.ID, "aws.lambda.Function", map[string]any{
		"name":    fnName,
		"runtime": u.Runtime,
		"handler": u.Handler,
		"role":    ir.Ref{Key: roleKey, Prop: "arn"},
		"code":    ir.Raw(fmt.Sprintf("new pulumi.asset.FileArchive(%q)", u.Artifact)),
		"timeout": 30,
		// 512 MB is enough for a FastAPI app under Mangum and keeps cold
		// starts short; raise it through pulumi_params if a unit needs more.
		"memorySize":  512,
		"environment": ir.Raw("{ variables: " + envVarsPlaceholder(u.ID) + " }"),
	}, nil)
	r.Program.Resolve(u.Key(), fn)
	r.Program.Connect(fnKey, roleKey, ir.EdgeDependsOn)
	r.Program.Connect(fnKey, logsKey, ir.EdgeDependsOn)
	for _, dep := range r.usedResourceKeys(u) {
		r.Program.Connect(fnKey, dep, ir.EdgeUses)
	}
	return nil
}

// envVarsPlaceholder names the generated const holding a unit's environment.
// The IaC backend emits that const from the unit's uses edges (D17), so the
// resolver does not have to know how environments are rendered.
func envVarsPlaceholder(unitID string) string {
	return sanitize.Identifier(unitID) + "Env"
}

// EnvConstName is the generated TypeScript const holding a unit's environment.
func EnvConstName(unitID string) string { return envVarsPlaceholder(unitID) }

// ---------------------------------------------------------------- gateway

func (r *Resolver) expose(e *ir.Expose) error {
	switch e.Config().Type {
	case "apigateway":
		return r.apiGatewayV2(e)
	case "alb":
		return r.alb(e)
	}
	return fmt.Errorf("no AWS mapping for expose type %q", e.Config().Type)
}

// apiGatewayV2 builds an HTTP API in front of a Lambda unit.
//
// HTTP APIs need far fewer resources than REST APIs -- no deployment or stage
// soup -- and pair cleanly with Mangum (D16). A single $default route forwards
// every path to the function, which is what lets FastAPI keep owning routing;
// the discovered routes are recorded in the IR for the topology rather than
// being duplicated as gateway resources.
func (r *Resolver) apiGatewayV2(e *ir.Expose) error {
	apiKey := ir.Key{Kind: KindAPIGatewayV2, ID: e.ID}
	fnKey := ir.Key{Kind: KindLambda, ID: e.Unit}
	if _, ok := r.Program.Resource(fnKey); !ok {
		return fmt.Errorf("expose %q fronts execution unit %q, which did not resolve to a Lambda function", e.ID, e.Unit)
	}

	api := ir.NewResource(KindAPIGatewayV2, e.ID, "aws.apigatewayv2.Api", map[string]any{
		"name":         r.uniqueName("apigateway", e.ID, sanitize.APIName),
		"protocolType": "HTTP",
	}, ir.Env(EnvGatewayURL(e.ID), "apiEndpoint"))
	r.Program.Resolve(e.Key(), api)

	integration := ir.NewResource(KindAPIIntegration, e.ID, "aws.apigatewayv2.Integration", map[string]any{
		"apiId":                ir.Ref{Key: apiKey, Prop: "id"},
		"integrationType":      "AWS_PROXY",
		"integrationUri":       ir.Ref{Key: fnKey, Prop: "arn"},
		"integrationMethod":    "POST",
		"payloadFormatVersion": "2.0",
	}, nil)
	r.Program.Resolve(e.Key(), integration)
	r.Program.Connect(integration.Key(), apiKey, ir.EdgeDependsOn)
	r.Program.Connect(integration.Key(), fnKey, ir.EdgeDependsOn)

	route := ir.NewResource(KindAPIRoute, e.ID, "aws.apigatewayv2.Route", map[string]any{
		"apiId":    ir.Ref{Key: apiKey, Prop: "id"},
		"routeKey": "$default",
		"target": ir.Lit("integrations/",
			ir.Ref{Key: integration.Key(), Prop: "id"}),
	}, nil)
	r.Program.Resolve(e.Key(), route)
	r.Program.Connect(route.Key(), integration.Key(), ir.EdgeDependsOn)

	stage := ir.NewResource(KindAPIStage, e.ID, "aws.apigatewayv2.Stage", map[string]any{
		"apiId":      ir.Ref{Key: apiKey, Prop: "id"},
		"name":       "$default",
		"autoDeploy": true,
	}, nil)
	r.Program.Resolve(e.Key(), stage)
	r.Program.Connect(stage.Key(), routeKeyOf(e), ir.EdgeDependsOn)

	permission := ir.NewResource(KindLambdaPerm, e.ID, "aws.lambda.Permission", map[string]any{
		"action":    "lambda:InvokeFunction",
		"function":  ir.Ref{Key: fnKey, Prop: "name"},
		"principal": "apigateway.amazonaws.com",
		"sourceArn": ir.Lit(ir.Ref{Key: apiKey, Prop: "executionArn"}, "/*/*"),
		"statementId": r.uniqueName("lambda-permission", e.ID,
			func(app, id string) string { return sanitize.StatementID(app, id, "invoke") }),
	}, nil)
	r.Program.Resolve(e.Key(), permission)
	r.Program.Connect(permission.Key(), apiKey, ir.EdgeDependsOn)
	r.Program.Connect(permission.Key(), fnKey, ir.EdgeDependsOn)
	return nil
}

func routeKeyOf(e *ir.Expose) ir.Key { return ir.Key{Kind: KindAPIRoute, ID: e.ID} }

// ---------------------------------------------------------------- pub/sub

// subscriptions wires each subscriber unit to the topics it listens on. This
// runs after both compute and topics exist, because a subscription references
// both. The generated Lambda entrypoint routes the delivered records to the
// handlers the unit registered.
func (r *Resolver) subscriptions() error {
	for _, in := range r.Program.IntentsOfKind(config.KindExecutionUnit) {
		unit := in.(*ir.ExecUnit)
		fnKey := ir.Key{Kind: KindLambda, ID: unit.ID}
		if _, ok := r.Program.Resource(fnKey); !ok {
			continue // a non-Lambda unit subscribes through its own runtime
		}
		for _, edge := range r.Program.EdgesFrom(unit.Key(), ir.EdgeSubscribes) {
			for _, topic := range r.Program.ResolvedFrom(edge.To) {
				if topic.Key().Kind != KindSNSTopic {
					continue
				}
				id := unit.ID + "-" + edge.To.ID
				topicKey := topic.Key()

				sub := ir.NewResource(KindSNSSub, id, "aws.sns.TopicSubscription", map[string]any{
					"topic":    ir.Ref{Key: topicKey, Prop: "arn"},
					"protocol": "lambda",
					"endpoint": ir.Ref{Key: fnKey, Prop: "arn"},
				}, nil)
				r.Program.Resolve(edge.To, sub)
				r.Program.Connect(sub.Key(), topicKey, ir.EdgeDependsOn)
				r.Program.Connect(sub.Key(), fnKey, ir.EdgeDependsOn)

				perm := ir.NewResource(KindLambdaPerm, id+"-sns", "aws.lambda.Permission", map[string]any{
					"action":    "lambda:InvokeFunction",
					"function":  ir.Ref{Key: fnKey, Prop: "name"},
					"principal": "sns.amazonaws.com",
					"sourceArn": ir.Ref{Key: topicKey, Prop: "arn"},
					"statementId": r.uniqueName("lambda-permission", id,
						func(app, key string) string { return sanitize.StatementID(app, key, "sns") }),
				}, nil)
				r.Program.Resolve(edge.To, perm)
				r.Program.Connect(perm.Key(), topicKey, ir.EdgeDependsOn)
				r.Program.Connect(perm.Key(), fnKey, ir.EdgeDependsOn)
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------- IAM

// actionsByKind is the least-privilege action set each capability grants. The
// policy is derived from the unit's `uses` edges, so a unit can only reach the
// stores its code actually declares.
var actionsByKind = map[string][]string{
	config.KindPersistKV: {
		"dynamodb:GetItem", "dynamodb:PutItem", "dynamodb:DeleteItem",
		"dynamodb:Query", "dynamodb:Scan", "dynamodb:BatchGetItem", "dynamodb:BatchWriteItem",
	},
	config.KindPersistFS: {
		"s3:GetObject", "s3:PutObject", "s3:DeleteObject", "s3:ListBucket",
	},
	config.KindPersistSecret: {
		"secretsmanager:GetSecretValue", "secretsmanager:PutSecretValue",
	},
	config.KindPersistORM: {
		"secretsmanager:GetSecretValue",
	},
	config.KindStaticUnit: {
		"s3:GetObject", "s3:ListBucket",
	},
}

// unitPolicy builds the inline role policy for one execution unit from its
// uses edges. Returns nil when the unit uses nothing that needs permission.
func (r *Resolver) unitPolicy(u *ir.ExecUnit, roleKey ir.Key) *ir.GenericResource {
	var statements []any

	for _, e := range r.Program.EdgesFrom(u.Key(), ir.EdgeUses) {
		for _, res := range r.Program.ResolvedFrom(e.To) {
			actions := r.actionsFor(e.To.Kind, res.Key().Kind)
			if len(actions) == 0 {
				continue
			}
			statements = append(statements, map[string]any{
				"Effect":   "Allow",
				"Action":   toAny(actions),
				"Resource": resourceScope(res.Key()),
			})
		}
	}
	// Publishing is a separate grant from reading, and only units that
	// actually publish get it.
	for _, e := range r.Program.EdgesFrom(u.Key(), ir.EdgePublishes) {
		for _, res := range r.Program.ResolvedFrom(e.To) {
			if res.Key().Kind != KindSNSTopic {
				continue
			}
			statements = append(statements, map[string]any{
				"Effect":   "Allow",
				"Action":   []any{"sns:Publish"},
				"Resource": []any{ir.Ref{Key: res.Key(), Prop: "arn"}},
			})
		}
	}

	if len(statements) == 0 {
		return nil
	}
	return ir.NewResource(KindLambdaPolicy, u.ID, "aws.iam.RolePolicy", map[string]any{
		"name": sanitize.IAMName(r.App, u.ID, "policy"),
		"role": ir.Ref{Key: roleKey, Prop: "id"},
		"policy": ir.JSONDoc{Value: map[string]any{
			"Version":   "2012-10-17",
			"Statement": statements,
		}},
	}, nil)
}

// actionsFor returns the granted actions for an intent kind, given the
// concrete resource it resolved to. Resources with no data plane -- a website
// configuration, a log group -- grant nothing.
func (r *Resolver) actionsFor(intentKind, resourceKind string) []string {
	switch resourceKind {
	case KindDynamoTable, KindS3Bucket, KindSecret, KindRDS:
		return actionsByKind[intentKind]
	}
	return nil
}

// resourceScope narrows a policy statement to exactly the resource it grants
// access to, plus the sub-resources that access implies.
func resourceScope(key ir.Key) []any {
	arn := ir.Ref{Key: key, Prop: "arn"}
	switch key.Kind {
	case KindDynamoTable:
		// Queries against a secondary index are authorised against the index
		// ARN, not the table's.
		return []any{arn, ir.Lit(arn, "/index/*")}
	case KindS3Bucket:
		// ListBucket is authorised on the bucket; object actions on its keys.
		return []any{arn, ir.Lit(arn, "/*")}
	case KindRDS:
		return []any{ir.Ref{Key: key, Prop: "masterUserSecrets.apply(s => s[0].secretArn)"}}
	}
	return []any{arn}
}

// usedResourceKeys returns the concrete resources a unit uses, sorted, so the
// generated project orders creation correctly.
func (r *Resolver) usedResourceKeys(u *ir.ExecUnit) []ir.Key {
	var out []ir.Key
	for _, e := range r.Program.EdgesFrom(u.Key(), ir.EdgeUses) {
		for _, res := range r.Program.ResolvedFrom(e.To) {
			out = append(out, res.Key())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

func toAny(s []string) []any {
	out := make([]any, 0, len(s))
	for _, v := range s {
		out = append(out, v)
	}
	return out
}
