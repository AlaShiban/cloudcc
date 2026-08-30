// Package pulumi_ts emits a Pulumi TypeScript project.
//
// Generation is uniformly data-driven (D11): every concrete resource type is a
// row in the registry below, giving its Pulumi class, the module to import,
// and the suffix its generated variable gets. Klotho 1 hand-wrote a TypeScript
// module per resource type, which meant supporting a new resource meant
// writing new TypeScript; here it means adding a row.
package pulumi_ts

import (
	"fmt"
	"sort"
)

// Template describes how to emit one concrete resource type.
type Template struct {
	// ID matches ir.Resource.Template(), e.g. "aws.dynamodb.Table".
	ID string
	// Class is the Pulumi class to construct.
	Class string
	// Import is the module alias the class comes from.
	Import string
	// VarSuffix disambiguates the generated variable name, so the Lambda and
	// the IAM role for the same unit id do not collide.
	VarSuffix string
	// URLProp, when set, names the property worth exporting as a stack output
	// for that resource.
	URLProp string
	// Func, when set, makes this a data source: the generated statement calls
	// Func(props) instead of constructing Class. Data sources have no Pulumi
	// resource name, which is why they need their own row shape.
	Func string
}

// templates is the whole registry. Adding a resource type is adding a row.
var templates = []Template{
	{ID: "aws.dynamodb.Table", Class: "aws.dynamodb.Table", Import: "aws", VarSuffix: "Table"},
	{ID: "aws.s3.BucketV2", Class: "aws.s3.BucketV2", Import: "aws", VarSuffix: "Bucket"},
	{ID: "aws.s3.BucketWebsiteConfigurationV2", Class: "aws.s3.BucketWebsiteConfigurationV2", Import: "aws", VarSuffix: "Website", URLProp: "websiteEndpoint"},
	{ID: "aws.s3.BucketObject", Class: "aws.s3.BucketObject", Import: "aws", VarSuffix: "Object"},
	{ID: "aws.s3.BucketPolicy", Class: "aws.s3.BucketPolicy", Import: "aws", VarSuffix: "BucketPolicy"},
	{ID: "aws.cloudfront.Distribution", Class: "aws.cloudfront.Distribution", Import: "aws", VarSuffix: "Cdn", URLProp: "domainName"},
	{ID: "aws.cloudfront.OriginAccessIdentity", Class: "aws.cloudfront.OriginAccessIdentity", Import: "aws", VarSuffix: "Oai"},
	{ID: "aws.secretsmanager.Secret", Class: "aws.secretsmanager.Secret", Import: "aws", VarSuffix: "Secret"},
	{ID: "aws.sns.Topic", Class: "aws.sns.Topic", Import: "aws", VarSuffix: "Topic"},
	{ID: "aws.sns.TopicSubscription", Class: "aws.sns.TopicSubscription", Import: "aws", VarSuffix: "Subscription"},
	{ID: "aws.sqs.Queue", Class: "aws.sqs.Queue", Import: "aws", VarSuffix: "Queue", URLProp: "url"},
	{ID: "aws.lambda.EventSourceMapping", Class: "aws.lambda.EventSourceMapping", Import: "aws", VarSuffix: "EventSource"},
	{ID: "aws.rds.Instance", Class: "aws.rds.Instance", Import: "aws", VarSuffix: "Db"},
	{ID: "aws.elasticache.Cluster", Class: "aws.elasticache.Cluster", Import: "aws", VarSuffix: "Cache"},
	{ID: "aws.memorydb.Cluster", Class: "aws.memorydb.Cluster", Import: "aws", VarSuffix: "Cache"},
	{ID: "aws.iam.Role", Class: "aws.iam.Role", Import: "aws", VarSuffix: "Role"},
	{ID: "aws.iam.RolePolicy", Class: "aws.iam.RolePolicy", Import: "aws", VarSuffix: "Policy"},
	{ID: "aws.cloudwatch.LogGroup", Class: "aws.cloudwatch.LogGroup", Import: "aws", VarSuffix: "Logs"},
	{ID: "aws.lambda.Function", Class: "aws.lambda.Function", Import: "aws", VarSuffix: "Fn"},
	{ID: "aws.lambda.Permission", Class: "aws.lambda.Permission", Import: "aws", VarSuffix: "Permission"},
	{ID: "aws.apigatewayv2.Api", Class: "aws.apigatewayv2.Api", Import: "aws", VarSuffix: "Api", URLProp: "apiEndpoint"},
	{ID: "aws.apigatewayv2.Integration", Class: "aws.apigatewayv2.Integration", Import: "aws", VarSuffix: "Integration"},
	{ID: "aws.apigatewayv2.Route", Class: "aws.apigatewayv2.Route", Import: "aws", VarSuffix: "Route"},
	{ID: "aws.apigatewayv2.Stage", Class: "aws.apigatewayv2.Stage", Import: "aws", VarSuffix: "Stage"},
	{ID: "aws.ecr.Repository", Class: "aws.ecr.Repository", Import: "aws", VarSuffix: "Repo"},
	{ID: "aws.ecs.Cluster", Class: "aws.ecs.Cluster", Import: "aws", VarSuffix: "Cluster"},
	{ID: "aws.ecs.TaskDefinition", Class: "aws.ecs.TaskDefinition", Import: "aws", VarSuffix: "Task"},
	{ID: "aws.ecs.Service", Class: "aws.ecs.Service", Import: "aws", VarSuffix: "Service"},
	{ID: "aws.lb.LoadBalancer", Class: "aws.lb.LoadBalancer", Import: "aws", VarSuffix: "Alb", URLProp: "dnsName"},
	{ID: "aws.lb.TargetGroup", Class: "aws.lb.TargetGroup", Import: "aws", VarSuffix: "TargetGroup"},
	{ID: "aws.lb.Listener", Class: "aws.lb.Listener", Import: "aws", VarSuffix: "Listener"},
	{ID: "aws.ec2.Vpc", Class: "aws.ec2.Vpc", Import: "aws", VarSuffix: "Vpc"},
	{ID: "aws.ec2.Subnet", Class: "aws.ec2.Subnet", Import: "aws", VarSuffix: "Subnet"},
	{ID: "aws.ec2.SecurityGroup", Class: "aws.ec2.SecurityGroup", Import: "aws", VarSuffix: "SecurityGroup"},
	{ID: "aws.ec2.InternetGateway", Class: "aws.ec2.InternetGateway", Import: "aws", VarSuffix: "Gateway"},
	{ID: "aws.ec2.RouteTable", Class: "aws.ec2.RouteTable", Import: "aws", VarSuffix: "Routes"},
	{ID: "aws.ec2.RouteTableAssociation", Class: "aws.ec2.RouteTableAssociation", Import: "aws", VarSuffix: "RouteAssoc"},
	{ID: "aws.rds.SubnetGroup", Class: "aws.rds.SubnetGroup", Import: "aws", VarSuffix: "SubnetGroup"},
	{ID: "aws.elasticache.SubnetGroup", Class: "aws.elasticache.SubnetGroup", Import: "aws", VarSuffix: "SubnetGroup"},
	{ID: "aws.memorydb.SubnetGroup", Class: "aws.memorydb.SubnetGroup", Import: "aws", VarSuffix: "SubnetGroup"},
	{ID: "aws.getAvailabilityZones", Func: "aws.getAvailabilityZonesOutput", Import: "aws", VarSuffix: "Zones"},

	// Kubernetes. The cluster and its node group are AWS resources; what runs
	// on them is not, and comes from a second provider built out of the
	// cluster's own endpoint and certificate authority. That is the reason
	// resources carry Pulumi options at all: every row below is created by
	// `k8sProvider`, and every row above by the ambient AWS one.
	{ID: "aws.eks.Cluster", Class: "aws.eks.Cluster", Import: "aws", VarSuffix: "Eks", URLProp: "endpoint"},
	{ID: "aws.eks.NodeGroup", Class: "aws.eks.NodeGroup", Import: "aws", VarSuffix: "Nodes"},
	{ID: "k8s.Provider", Class: "k8s.Provider", Import: "k8s", VarSuffix: "K8s"},
	{ID: "k8s.apps.v1.Deployment", Class: "k8s.apps.v1.Deployment", Import: "k8s", VarSuffix: "Deployment"},
	{ID: "k8s.core.v1.Service", Class: "k8s.core.v1.Service", Import: "k8s", VarSuffix: "Svc"},
}

var byID = func() map[string]Template {
	out := map[string]Template{}
	for _, t := range templates {
		if _, dup := out[t.ID]; dup {
			panic(fmt.Sprintf("pulumi_ts: duplicate resource template %q", t.ID))
		}
		out[t.ID] = t
	}
	return out
}()

// Lookup returns the template for a resource type. An unregistered type is an
// error rather than a guess: emitting a class that does not exist would only
// fail later, at `pulumi up`, with a far worse message.
func Lookup(id string) (Template, error) {
	t, ok := byID[id]
	if !ok {
		return Template{}, fmt.Errorf("no Pulumi template registered for resource type %q", id)
	}
	return t, nil
}

// TemplateIDs returns every registered type, sorted.
func TemplateIDs() []string {
	out := make([]string, 0, len(byID))
	for id := range byID {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
