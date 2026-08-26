package topology

import (
	"fmt"
	"sort"
	"strings"

	"github.com/cloudcompiler/cloudcc/internal/config"
	"github.com/cloudcompiler/cloudcc/internal/ir"
)

// PythonFile is the generated diagrams-as-code program.
//
// Mermaid and DOT describe the architecture in their own notations; this one
// describes it in Python, using mingrammer's `diagrams` package, and renders
// with the provider's own icons. It is worth having as a *file* rather than
// only as an image: it is the one output someone can edit -- move a cluster,
// drop the IAM noise, add an annotation for a review -- without hand-drawing
// anything, and it can be diffed in a pull request like the rest of the
// compiled tree.
const PythonFile = "architecture.py"

// diagramsNode maps a concrete resource kind to a class in the diagrams
// package.
//
// Every name here was read out of the installed package rather than guessed,
// because a wrong one is an ImportError at render time and the picture simply
// never appears. Anything unmapped falls back to a generic node, which is
// honest: an icon that is merely close is worse than an obviously plain box.
var diagramsNode = map[string]string{
	"aws.lambda": "aws.compute.Lambda",

	"aws.ecs.cluster":        "aws.compute.ECS",
	"aws.ecs.service":        "aws.compute.ElasticContainerServiceService",
	"aws.ecs.taskdefinition": "aws.compute.ElasticContainerServiceTask",
	"aws.ecr":                "aws.compute.ECR",

	"aws.iam.role":          "aws.security.IAMRole",
	"aws.iam.policy":        "aws.security.IAMPermissions",
	"aws.ecs.execrole":      "aws.security.IAMRole",
	"aws.ecs.taskrole":      "aws.security.IAMRole",
	"aws.ecs.taskpolicy":    "aws.security.IAMPermissions",
	"aws.lambda.permission": "aws.security.IAMPermissions",
	"aws.secretsmanager":    "aws.security.SecretsManager",

	"aws.cloudwatch.loggroup": "aws.management.CloudwatchLogs",

	"aws.apigatewayv2":             "aws.network.APIGateway",
	"aws.apigatewayv2.integration": "aws.network.APIGatewayEndpoint",
	"aws.apigatewayv2.route":       "aws.network.APIGatewayEndpoint",
	"aws.apigatewayv2.stage":       "aws.network.APIGatewayEndpoint",
	"aws.alb":                      "aws.network.ElbApplicationLoadBalancer",
	"aws.alb.targetgroup":          "aws.network.ELB",
	"aws.alb.listener":             "aws.network.ELB",
	"aws.vpc":                      "aws.network.VPC",
	"aws.vpc.subnet":               "aws.network.PrivateSubnet",
	"aws.vpc.securitygroup":        "aws.network.Nacl",
	"aws.vpc.gateway":              "aws.network.InternetGateway",
	"aws.vpc.routetable":           "aws.network.RouteTable",
	"aws.vpc.routeassoc":           "aws.network.RouteTable",

	"aws.dynamodb":    "aws.database.Dynamodb",
	"aws.rds":         "aws.database.RDS",
	"aws.elasticache": "aws.database.ElastiCache",
	"aws.memorydb":    "aws.database.ElasticacheForRedis",

	"aws.s3":         "aws.storage.S3",
	"aws.s3.object":  "aws.storage.SimpleStorageServiceS3Object",
	"aws.s3.website": "aws.storage.SimpleStorageServiceS3BucketWithObjects",

	"aws.sns":              "aws.integration.SNS",
	"aws.sns.subscription": "aws.integration.EventResource",
	"aws.sqs":              "aws.integration.SQS",
	"aws.kinesis":          "aws.analytics.KinesisDataStreams",
}

const fallbackNode = "aws.general.General"

// nodeClass returns the fully qualified diagrams class for a resource kind.
func nodeClass(kind string) string {
	if cls, ok := diagramsNode[kind]; ok {
		return cls
	}
	return fallbackNode
}

// nodeName is what the class is called once imported, which is what a call
// site must use: `from diagrams.aws.compute import Lambda` binds Lambda, not
// aws.compute.Lambda.
func nodeName(kind string) string {
	cls := nodeClass(kind)
	return cls[strings.LastIndex(cls, ".")+1:]
}

// serviceLabel is how a resource is named under its icon. "Lambda" and
// "DynamoDB" are what the picture is about; "aws.apigatewayv2.integration" is
// what the picture is made of, and those are different questions.
var serviceLabel = map[string]string{
	"aws.lambda":         "Lambda",
	"aws.ecs.service":    "Fargate",
	"aws.apigatewayv2":   "API Gateway",
	"aws.alb":            "Load Balancer",
	"aws.dynamodb":       "DynamoDB",
	"aws.s3":             "S3",
	"aws.s3.website":     "S3 website",
	"aws.rds":            "RDS",
	"aws.elasticache":    "ElastiCache",
	"aws.memorydb":       "MemoryDB",
	"aws.secretsmanager": "Secrets Manager",
	"aws.sns":            "SNS",
	"aws.sqs":            "SQS",
	"aws.kinesis":        "Kinesis",
}

// principalKinds ranks the resource kinds worth an icon, highest first. Every
// capability expands into several resources -- a Lambda comes with a role, a
// policy and a log group; an HTTP API with an integration, a route and a stage
// -- and exactly one of them is what the architecture is made of.
var principalKinds = []string{
	"aws.lambda",
	"aws.ecs.service",
	"aws.apigatewayv2",
	"aws.alb",
	"aws.rds",
	"aws.elasticache",
	"aws.memorydb",
	"aws.dynamodb",
	"aws.sns",
	"aws.sqs",
	"aws.kinesis",
	"aws.secretsmanager",
	"aws.s3.website",
	"aws.s3",
}

// Python renders the architecture as a diagrams program.
//
// **This one draws the architecture, not the resource graph.** One icon per
// capability -- the service it resolved to -- and the edges the program itself
// declared: a gateway in front of a unit, a unit reading a store, a unit
// publishing to a topic. What it leaves out is the supporting cast: IAM roles
// and policies, log groups, an HTTP API's integration and route and stage, VPC
// routing tables.
//
// That is the difference between an architecture diagram and a dependency
// graph. Nobody draws the execution role when they sketch a service on a
// whiteboard, and a picture in which a three-service application arrives as ten
// icons and fourteen dashed arrows is not one anybody reads twice.
//
// The exhaustive view has not gone anywhere: architecture.mmd and .dot carry
// every resolved resource, and the e2e harness checks them against what the
// stack actually created. Two files, two questions -- what is this system, and
// what will exist in the account.
func Python(p *ir.Program, opts Options) []byte {
	drawn := map[string]ir.Key{} // intent key -> the resource that represents it
	var order []ir.Intent
	for _, in := range p.Intents() {
		if in.Capability() == config.KindConfig {
			// A config value becomes an environment variable, not a resource.
			continue
		}
		res, ok := principalResource(p, in)
		if !ok {
			continue
		}
		drawn[in.Key().String()] = res
		order = append(order, in)
	}
	sort.Slice(order, func(i, j int) bool {
		return order[i].Key().String() < order[j].Key().String()
	})

	var b strings.Builder
	fmt.Fprintf(&b, `"""Architecture of %s, generated by cloudcc.

Diagram as code, using https://pypi.org/project/diagrams -- one icon per
capability, showing the service each one resolved to and how the program said
they are connected.

The supporting cast is deliberately absent: roles, policies, log groups and an
HTTP API's internal wiring all exist, and all of them are in architecture.mmd
and architecture.dot. This file answers "what is this system", not "what will
exist in the account".

    pip install diagrams   # and graphviz
    python %s

Recompiling overwrites this file. Copy it before editing.
"""

`, opts.App, PythonFile)

	b.WriteString("from diagrams import Diagram, Edge\n")
	keys := make([]ir.Key, 0, len(order))
	for _, in := range order {
		keys = append(keys, drawn[in.Key().String()])
	}
	for _, imp := range importsFor(keys) {
		b.WriteString(imp + "\n")
	}
	b.WriteString("\n")

	fmt.Fprintf(&b, "with Diagram(\n    %q,\n    filename=%q,\n    outformat=%q,\n    show=False,\n    direction=%q,\n):\n",
		opts.App, archPNGStem(opts.App), "png", "LR")

	vars := map[string]string{}
	for _, in := range order {
		res := drawn[in.Key().String()]
		name := pyVar(in.Key(), vars)
		fmt.Fprintf(&b, "    %s = %s(%q)\n", name, nodeName(res.Kind), pyLabel(in.Key(), res.Kind))
	}

	// A unit that publishes to a topic also *uses* it, and both edges are in
	// the graph. Drawing both puts two arrows between the same pair, one of
	// which says less than the other -- so the specific edge wins and the
	// generic one is dropped.
	said := map[string]bool{}
	for _, e := range p.Edges() {
		if e.Kind == ir.EdgePublishes || e.Kind == ir.EdgeSubscribes || e.Kind == ir.EdgeCalls {
			said[e.From.String()+"->"+e.To.String()] = true
		}
	}

	b.WriteString("\n")
	for _, e := range p.Edges() {
		from, ok := vars[e.From.String()]
		if !ok {
			continue
		}
		to, ok := vars[e.To.String()]
		if !ok {
			continue
		}
		if e.Kind == ir.EdgeUses && said[e.From.String()+"->"+e.To.String()] {
			continue
		}
		// A unit is wired to every topic declared in a module it bundles, so
		// that the client the module builds at import has an ARN to build it
		// with. That is a deployment fact, and it is in architecture.mmd. It is
		// not an architecture fact: a unit that neither publishes to a topic nor
		// subscribes to it is not part of that topic's flow, and an arrow saying
		// otherwise would have a reader looking for a message that is not there.
		if e.Kind == ir.EdgeUses && e.To.Kind == config.KindPubSub {
			continue
		}
		switch e.Kind {
		case ir.EdgeExposes, ir.EdgeUses:
			fmt.Fprintf(&b, "    %s >> %s\n", from, to)
		case ir.EdgeCalls:
			// Bold, and pointing the way the request goes. This is the only
			// edge in the picture the caller waits on, and a reader tracing a
			// latency budget needs to be able to see which arrows those are.
			fmt.Fprintf(&b, "    %s >> Edge(label=%q, color=%q, style=%q) >> %s\n",
				from, "calls", "darkorange", "bold", to)
		case ir.EdgePublishes:
			fmt.Fprintf(&b, "    %s >> Edge(label=%q) >> %s\n", from, "publishes", to)
		case ir.EdgeSubscribes:
			// Recorded as unit -> topic, because that is where the call is
			// written; drawn topic -> unit, because that is where the message
			// goes. A flow diagram pointing the other way would be saying
			// something untrue about runtime.
			fmt.Fprintf(&b, "    %s >> Edge(label=%q, style=%q) >> %s\n", to, "subscribes", "dashed", from)
		}
	}
	return []byte(b.String())
}

// principalResource returns the one resource that represents an intent in an
// architecture diagram, and whether there is one worth drawing.
func principalResource(p *ir.Program, in ir.Intent) (ir.Key, bool) {
	byKind := map[string]ir.Key{}
	for _, res := range p.ResolvedFrom(in.Key()) {
		if _, seen := byKind[res.Key().Kind]; !seen {
			byKind[res.Key().Kind] = res.Key()
		}
	}
	for _, kind := range principalKinds {
		if key, ok := byKind[kind]; ok {
			return key, true
		}
	}
	return ir.Key{}, false
}

// importsFor returns the sorted import lines the drawn nodes need.
func importsFor(keys []ir.Key) []string {
	byModule := map[string]map[string]bool{}
	for _, key := range keys {
		cls := nodeClass(key.Kind)
		i := strings.LastIndex(cls, ".")
		module, name := cls[:i], cls[i+1:]
		if byModule[module] == nil {
			byModule[module] = map[string]bool{}
		}
		byModule[module][name] = true
	}

	out := make([]string, 0, len(byModule))
	for module, names := range byModule {
		sorted := make([]string, 0, len(names))
		for name := range names {
			sorted = append(sorted, name)
		}
		sort.Strings(sorted)
		out = append(out, fmt.Sprintf("from diagrams.%s import %s", module, strings.Join(sorted, ", ")))
	}
	sort.Strings(out)
	return out
}

// pyVar assigns a unique Python identifier to a resource and remembers it.
func pyVar(key ir.Key, seen map[string]string) string {
	base := "n_" + nodeID(key)
	name := base
	for i := 2; used(seen, name); i++ {
		name = fmt.Sprintf("%s_%d", base, i)
	}
	seen[key.String()] = name
	return name
}

func used(seen map[string]string, name string) bool {
	for _, v := range seen {
		if v == name {
			return true
		}
	}
	return false
}

// pyLabel is the caption under an icon: what the program called it, and which
// service it became.
func pyLabel(key ir.Key, resourceKind string) string {
	label, ok := serviceLabel[resourceKind]
	if !ok {
		label = serviceName(resourceKind)
	}
	return key.ID + "\n" + label
}

// archPNGStem is what the diagrams program passes as `filename`; the package
// appends the format itself.
func archPNGStem(app string) string { return app + "-architecture" }
