package aws

import (
	"github.com/cloudcompiler/cloudcc/internal/ir"
	"github.com/cloudcompiler/cloudcc/internal/sanitize"
)

// ClusterID is the shared ECS cluster every containerised unit runs in.
const ClusterID = "cluster"

// ContainerPort is the port a generated container listens on. The generated
// Dockerfile and the target group's health check both use it.
const ContainerPort = 8080

// ecsService expands one execution unit into a Fargate service.
//
// The container image itself is not built here: cloudcc emits a Dockerfile (or
// respects the user's) and an ECR repository, and bin/package.sh builds and
// pushes when Docker is available. Making Pulumi build images would pull in
// another provider for something the packaging script already owns.
func (r *Resolver) ecsServiceUnit(u *ir.ExecUnit) error {
	r.network()

	repoKey := ir.Key{Kind: KindECRRepo, ID: u.ID}
	// Distinct ids, not just distinct kinds: both resolve to aws.iam.Role, and
	// Pulumi names resources per type, so sharing an id would collide.
	execRoleID := u.ID + "-exec"
	taskRoleID := u.ID + "-task"
	execRoleKey := ir.Key{Kind: KindECSExecRole, ID: execRoleID}
	taskRoleKey := ir.Key{Kind: KindECSTaskRole, ID: taskRoleID}
	taskKey := ir.Key{Kind: KindECSTask, ID: u.ID}
	logsKey := ir.Key{Kind: KindLogGroup, ID: u.ID}
	clusterKey := ir.Key{Kind: KindECSCluster, ID: ClusterID}

	// The repository URL is exported so bin/push-images.sh can find where to
	// push, without the script having to know how names are generated.
	repo := ir.NewResource(KindECRRepo, u.ID, "aws.ecr.Repository", map[string]any{
		"name":        sanitize.ElastiCacheCluster(r.App, u.ID),
		"forceDelete": true,
	}, ir.Env(EnvECRRepo(u.ID), "repositoryUrl"))
	r.Program.Resolve(u.Key(), repo)

	if _, done := r.Program.Resource(clusterKey); !done {
		cluster := ir.NewResource(KindECSCluster, ClusterID, "aws.ecs.Cluster", map[string]any{
			"name": sanitize.LambdaFunction(r.App, ClusterID),
		}, nil)
		r.Program.AddResource(cluster)
	}
	r.Program.Connect(u.Key(), clusterKey, ir.EdgeDependsOn)

	logs := ir.NewResource(KindLogGroup, u.ID, "aws.cloudwatch.LogGroup", map[string]any{
		"name":            "/ecs/" + sanitize.LambdaFunction(r.App, u.ID),
		"retentionInDays": 14,
	}, nil)
	r.Program.Resolve(u.Key(), logs)

	// Two roles, deliberately: the execution role is what ECS itself uses to
	// pull the image and write logs, the task role is what the application
	// code gets. Merging them would hand the application permissions it has no
	// business holding.
	execRole := ir.NewResource(KindECSExecRole, execRoleID, "aws.iam.Role", map[string]any{
		"name":             sanitize.IAMName(r.App, u.ID, "exec"),
		"assumeRolePolicy": ecsAssumeRolePolicy(),
		"managedPolicyArns": []any{
			"arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy",
		},
	}, nil)
	r.Program.Resolve(u.Key(), execRole)

	taskRole := ir.NewResource(KindECSTaskRole, taskRoleID, "aws.iam.Role", map[string]any{
		"name":             sanitize.IAMName(r.App, u.ID, "task"),
		"assumeRolePolicy": ecsAssumeRolePolicy(),
	}, nil)
	r.Program.Resolve(u.Key(), taskRole)

	if policy := r.unitPolicy(u, taskRoleKey); policy != nil {
		policy.K = ir.Key{Kind: KindECSTaskPolicy, ID: u.ID}
		r.Program.Resolve(u.Key(), policy)
	}

	task := ir.NewResource(KindECSTask, u.ID, "aws.ecs.TaskDefinition", map[string]any{
		"family":                  sanitize.LambdaFunction(r.App, u.ID),
		"requiresCompatibilities": []any{"FARGATE"},
		"networkMode":             "awsvpc",
		"cpu":                     "256",
		"memory":                  "512",
		"executionRoleArn":        ir.Ref{Key: execRoleKey, Prop: "arn"},
		"taskRoleArn":             ir.Ref{Key: taskRoleKey, Prop: "arn"},
		"containerDefinitions": ir.JSONDoc{Value: []any{map[string]any{
			"name":  u.ID,
			"image": ir.Lit(ir.Ref{Key: repoKey, Prop: "repositoryUrl"}, ":latest"),
			"portMappings": []any{map[string]any{
				"containerPort": ContainerPort,
				"protocol":      "tcp",
			}},
			"environment": ir.Raw(envAsNameValueList(u.ID)),
			"logConfiguration": map[string]any{
				"logDriver": "awslogs",
				"options": map[string]any{
					"awslogs-group":         ir.Ref{Key: logsKey, Prop: "name"},
					"awslogs-region":        ir.Raw("aws.config.region"),
					"awslogs-stream-prefix": u.ID,
				},
			},
		}}},
	}, nil)
	r.Program.Resolve(u.Key(), task)
	// The task definition is what carries the unit's environment, so it is
	// what must be declared after everything that environment references.
	for _, dep := range r.usedResourceKeys(u) {
		r.Program.Connect(task.Key(), dep, ir.EdgeUses)
	}

	service := ir.NewResource(KindECSService, u.ID, "aws.ecs.Service", map[string]any{
		"name":           sanitize.LambdaFunction(r.App, u.ID),
		"cluster":        ir.Ref{Key: clusterKey, Prop: "id"},
		"taskDefinition": ir.Ref{Key: taskKey, Prop: "arn"},
		"desiredCount":   1,
		"launchType":     "FARGATE",
		"networkConfiguration": map[string]any{
			"subnets":        subnetIDList(),
			"securityGroups": []any{ir.Ref{Key: securityGroupKey(), Prop: "id"}},
			"assignPublicIp": true,
		},
	}, nil)
	r.Program.Resolve(u.Key(), service)

	for _, dep := range r.usedResourceKeys(u) {
		r.Program.Connect(service.Key(), dep, ir.EdgeUses)
	}
	return nil
}

// envAsNameValueList renders a unit's environment as the [{name, value}] shape
// an ECS container definition wants, from the same const the Lambda path uses.
func envAsNameValueList(unitID string) string {
	return "Object.entries(" + EnvConstName(unitID) +
		").map(([name, value]) => ({ name, value }))"
}

func ecsAssumeRolePolicy() ir.JSONDoc {
	return ir.JSONDoc{Value: map[string]any{
		"Version": "2012-10-17",
		"Statement": []any{map[string]any{
			"Effect":    "Allow",
			"Action":    "sts:AssumeRole",
			"Principal": map[string]any{"Service": "ecs-tasks.amazonaws.com"},
		}},
	}}
}

// ---------------------------------------------------------------- ALB

// alb fronts a containerised unit with an application load balancer.
func (r *Resolver) alb(e *ir.Expose) error {
	r.network()

	serviceKey := ir.Key{Kind: KindECSService, ID: e.Unit}
	if _, ok := r.Program.Resource(serviceKey); !ok {
		return errUnsupported("expose %q uses an ALB, which can only front an ECS execution unit; %q is not one", e.ID, e.Unit)
	}
	albKey := ir.Key{Kind: KindALB, ID: e.ID}
	tgKey := ir.Key{Kind: KindALBTargetGroup, ID: e.ID}

	lb := ir.NewResource(KindALB, e.ID, "aws.lb.LoadBalancer", map[string]any{
		"name":             sanitize.ElastiCacheCluster(r.App, e.ID),
		"loadBalancerType": "application",
		"internal":         e.Target != "public",
		"subnets":          subnetIDList(),
		"securityGroups":   []any{ir.Ref{Key: securityGroupKey(), Prop: "id"}},
	}, ir.Env(EnvGatewayURL(e.ID), ir.FromExpr(ir.Lit("http://", ir.Ref{Key: albKey, Prop: "dnsName"}))))
	r.Program.Resolve(e.Key(), lb)

	tg := ir.NewResource(KindALBTargetGroup, e.ID, "aws.lb.TargetGroup", map[string]any{
		"name":       sanitize.ElastiCacheCluster(r.App, e.ID+"-tg"),
		"port":       ContainerPort,
		"protocol":   "HTTP",
		"targetType": "ip",
		"vpcId":      ir.Ref{Key: vpcKey(), Prop: "id"},
		"healthCheck": map[string]any{
			"path":    healthCheckPath(e),
			"matcher": "200-399",
		},
	}, nil)
	r.Program.Resolve(e.Key(), tg)

	listener := ir.NewResource(KindALBListener, e.ID, "aws.lb.Listener", map[string]any{
		"loadBalancerArn": ir.Ref{Key: albKey, Prop: "arn"},
		"port":            80,
		"protocol":        "HTTP",
		"defaultActions": []any{map[string]any{
			"type":           "forward",
			"targetGroupArn": ir.Ref{Key: tgKey, Prop: "arn"},
		}},
	}, nil)
	r.Program.Resolve(e.Key(), listener)

	// The service joins the target group, which is what actually puts traffic
	// on the containers.
	service, _ := r.Program.Resource(serviceKey)
	service.Props()["loadBalancers"] = []any{map[string]any{
		"targetGroupArn": ir.Ref{Key: tgKey, Prop: "arn"},
		"containerName":  e.Unit,
		"containerPort":  ContainerPort,
	}}
	r.Program.Connect(serviceKey, listener.Key(), ir.EdgeDependsOn)
	return nil
}

// healthCheckPath prefers a route the application actually serves, so a health
// check does not fail against a program that has no "/" handler.
func healthCheckPath(e *ir.Expose) string {
	for _, route := range e.Routes {
		if route.Verb == "GET" && route.Path == "/health" {
			return "/health"
		}
	}
	for _, route := range e.Routes {
		if route.Verb == "GET" && !hasPathParameter(route.Path) {
			return route.Path
		}
	}
	return "/"
}

func hasPathParameter(p string) bool {
	for i := 0; i < len(p); i++ {
		if p[i] == '{' || p[i] == ':' {
			return true
		}
	}
	return false
}
