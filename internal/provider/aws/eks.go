package aws

import (
	"github.com/cloudcompiler/cloudcc/internal/config"
	"github.com/cloudcompiler/cloudcc/internal/ir"
	"github.com/cloudcompiler/cloudcc/internal/sanitize"
)

// A container unit on Kubernetes.
//
// The same unit, the same image, the same environment as the Fargate path next
// door in ecs.go -- what changes is what runs it. `platform: kubernetes` picks
// this; `platform: serverless` (the default) picks that.
//
// # Two providers in one program
//
// The cluster and its node group are AWS resources. What runs *on* the cluster
// is not: a Deployment is created by the Kubernetes API, through a second
// Pulumi provider built from the cluster's own endpoint and certificate
// authority. That is why resources carry Pulumi options at all -- every
// Kubernetes resource below names `k8sProvider`, and every AWS resource beside
// it uses the ambient one.
//
// The kubeconfig is assembled the way `aws eks update-kubeconfig` assembles
// one: endpoint, CA data, and an exec plugin that calls `aws eks get-token`.
// Nothing about it is emulator-specific, which is the point -- the emulator
// answers `describe-cluster` with a real certificate authority and `get-token`
// with a real ExecCredential, so the same program works against both.
//
// # What this does not do yet
//
// A pod gets its bindings and not its permissions. On Fargate the task role is
// derived from what the unit's code reaches for; the Kubernetes equivalent is
// IRSA -- an OIDC provider on the cluster, a role trusting it, and a
// ServiceAccount annotated with that role -- and none of it is emitted. A unit
// that reaches an AWS store is warned about, rather than silently deployed
// without an identity.

// ClusterName is the shared EKS cluster every Kubernetes unit runs in. One
// cluster per application, like the ECS cluster next door: a cluster per unit
// would be minutes of provisioning each and nothing gained.
const ClusterName = "kubernetes"

// EnvKubeconfig lets a kubeconfig be supplied from outside, replacing the one
// assembled from the cluster's outputs.
//
// Needed because a local emulator provisions a Kubernetes cluster it cannot
// hand out EKS credentials for. LocalStack accepts the token
// `aws eks get-token` returns, so against it this variable is never set and the
// generated kubeconfig is used as written. Some emulators do not: k3s
// authenticates its own client certificates and rejects the token outright, and
// then the kubeconfig that is correct for AWS is rejected by the thing standing
// in for it. The same bargain as an RDS instance with no engine behind it, and
// answered the same way: the harness supplies what works, and the shape of the
// thing is still tested.
const EnvKubeconfig = "CLOUDCC_KUBECONFIG"

// eksUnit expands one execution unit into a Deployment on the shared cluster.
func (r *Resolver) eksUnit(u *ir.ExecUnit) error {
	r.network()
	r.eksCluster()

	repoKey := ir.Key{Kind: KindECRRepo, ID: u.ID}
	deployKey := ir.Key{Kind: KindK8sDeployment, ID: u.ID}
	providerKey := ir.Key{Kind: KindK8sProvider, ID: ClusterName}

	repo := ir.NewResource(KindECRRepo, u.ID, "aws.ecr.Repository", map[string]any{
		"name":        sanitize.ElastiCacheCluster(r.App, u.ID),
		"forceDelete": true,
	}, ir.Env(EnvECRRepo(u.ID), "repositoryUrl"))
	r.Program.Resolve(u.Key(), repo)

	cpu, memory, _, err := TaskDefinitionArgs(u.ID, u.Config(), 256, 512)
	if err != nil {
		return err
	}

	labels := map[string]any{"app": u.ID}
	deployment := ir.NewResource(KindK8sDeployment, u.ID, "k8s.apps.v1.Deployment", map[string]any{
		"metadata": map[string]any{
			"name":   sanitize.LambdaFunction(r.App, u.ID),
			"labels": labels,
		},
		"spec": map[string]any{
			"replicas": 1,
			"selector": map[string]any{"matchLabels": labels},
			"template": map[string]any{
				"metadata": map[string]any{"labels": labels},
				"spec": map[string]any{
					"containers": []any{map[string]any{
						"name":  u.ID,
						"image": ir.Lit(ir.Ref{Key: repoKey, Prop: "repositoryUrl"}, ":latest"),
						"ports": []any{map[string]any{"containerPort": ContainerPort}},
						// The same environment the Fargate path builds, in the
						// shape Kubernetes wants. A binding is a binding
						// wherever it is read; only the spelling differs.
						"env": ir.Raw(envAsK8sEnvVars(u.ID)),
						// Requests and limits from the same portable `memory:`
						// that sizes a task definition. Kubernetes writes
						// memory with a unit suffix and CPU in millicores,
						// where Fargate writes plain numbers -- so this is a
						// translation, not a second setting.
						"resources": map[string]any{
							"requests": map[string]any{
								"cpu":    millicores(cpu),
								"memory": memory + "Mi",
							},
							"limits": map[string]any{
								"cpu":    millicores(cpu),
								"memory": memory + "Mi",
							},
						},
					}},
				},
			},
		},
	}, nil).WithOpts(map[string]any{"provider": ir.Ref{Key: providerKey, Prop: ""}})
	r.Program.Resolve(u.Key(), deployment)
	r.Program.Connect(deployKey, providerKey, ir.EdgeDependsOn)
	r.Program.Connect(deployKey, repoKey, ir.EdgeDependsOn)

	// A Service so the Deployment has a stable address. ClusterIP unless
	// something exposes this unit, in which case the gateway needs a load
	// balancer in front of the pods.
	serviceType := "ClusterIP"
	if r.isExposed(u.ID) {
		serviceType = "LoadBalancer"
	}
	svcKey := ir.Key{Kind: KindK8sService, ID: u.ID}
	service := ir.NewResource(KindK8sService, u.ID, "k8s.core.v1.Service", map[string]any{
		"metadata": map[string]any{
			"name":   sanitize.LambdaFunction(r.App, u.ID),
			"labels": labels,
		},
		"spec": map[string]any{
			"type":     serviceType,
			"selector": labels,
			"ports": []any{map[string]any{
				"port":       80,
				"targetPort": ContainerPort,
				"protocol":   "TCP",
			}},
		},
	}, nil).WithOpts(map[string]any{
		"provider": ir.Ref{Key: providerKey, Prop: ""},
		// Ordered after the Deployment, explicitly. A Service names its pods by
		// label and never refers to the Deployment, so nothing else tells the
		// engine which comes first -- and a Service created before its pods
		// exist waits for endpoints that cannot arrive yet. With resources
		// created in parallel that resolves itself; created one at a time, the
		// Service goes first, waits its full timeout, and the Deployment is
		// never created at all.
		//
		// The edge below records the same thing in the IR, for the topology.
		// This one is what the engine acts on.
		"dependsOn": []any{ir.Ref{Key: deployKey, Prop: ""}},
	})
	r.Program.Resolve(u.Key(), service)
	r.Program.Connect(svcKey, deployKey, ir.EdgeDependsOn)

	return nil
}

// eksCluster creates the shared cluster, its roles, its node group and the
// Kubernetes provider, once per application.
func (r *Resolver) eksCluster() {
	clusterKey := ir.Key{Kind: KindEKSCluster, ID: ClusterName}
	if _, done := r.Program.Resource(clusterKey); done {
		return
	}

	clusterRoleKey := ir.Key{Kind: KindEKSClusterRole, ID: ClusterName}
	nodeRoleKey := ir.Key{Kind: KindEKSNodeRole, ID: ClusterName}
	nodesKey := ir.Key{Kind: KindEKSNodeGroup, ID: ClusterName}
	providerKey := ir.Key{Kind: KindK8sProvider, ID: ClusterName}

	clusterRole := ir.NewResource(KindEKSClusterRole, ClusterName, "aws.iam.Role", map[string]any{
		"name":             sanitize.IAMName(r.App, ClusterName, "cluster"),
		"assumeRolePolicy": servicePrincipalPolicy("eks.amazonaws.com"),
		"managedPolicyArns": []any{
			"arn:aws:iam::aws:policy/AmazonEKSClusterPolicy",
		},
	}, nil)
	r.Program.AddResource(clusterRole)

	// The node role is the one worth reading twice: it is what the *nodes*
	// hold, not what an application holds. Every pod on a node can reach it,
	// so it carries only what the kubelet needs -- joining the cluster, the CNI
	// and pulling images -- and nothing an application would want.
	nodeRole := ir.NewResource(KindEKSNodeRole, ClusterName, "aws.iam.Role", map[string]any{
		"name":             sanitize.IAMName(r.App, ClusterName, "node"),
		"assumeRolePolicy": servicePrincipalPolicy("ec2.amazonaws.com"),
		"managedPolicyArns": []any{
			"arn:aws:iam::aws:policy/AmazonEKSWorkerNodePolicy",
			"arn:aws:iam::aws:policy/AmazonEKS_CNI_Policy",
			"arn:aws:iam::aws:policy/AmazonEC2ContainerRegistryReadOnly",
		},
	}, nil)
	r.Program.AddResource(nodeRole)

	cluster := ir.NewResource(KindEKSCluster, ClusterName, "aws.eks.Cluster", map[string]any{
		"name":    sanitize.LambdaFunction(r.App, ClusterName),
		"roleArn": ir.Ref{Key: clusterRoleKey, Prop: "arn"},
		"vpcConfig": map[string]any{
			"subnetIds": subnetIDList(),
		},
	}, ir.Env(EnvClusterEndpoint(ClusterName), "endpoint"))
	r.Program.AddResource(cluster)
	r.Program.Connect(clusterKey, clusterRoleKey, ir.EdgeDependsOn)

	nodes := ir.NewResource(KindEKSNodeGroup, ClusterName, "aws.eks.NodeGroup", map[string]any{
		"clusterName":   ir.Ref{Key: clusterKey, Prop: "name"},
		"nodeGroupName": sanitize.LambdaFunction(r.App, ClusterName+"-nodes"),
		"nodeRoleArn":   ir.Ref{Key: nodeRoleKey, Prop: "arn"},
		"subnetIds":     subnetIDList(),
		"scalingConfig": map[string]any{
			"desiredSize": 1,
			"minSize":     1,
			"maxSize":     2,
		},
	}, nil)
	r.Program.AddResource(nodes)
	r.Program.Connect(nodesKey, clusterKey, ir.EdgeDependsOn)
	r.Program.Connect(nodesKey, nodeRoleKey, ir.EdgeDependsOn)

	// The kubeconfig, assembled exactly as `aws eks update-kubeconfig` builds
	// one. Written as an expression rather than a literal because two of its
	// three parts are outputs that do not exist until the cluster does.
	provider := ir.NewResource(KindK8sProvider, ClusterName, "k8s.Provider", map[string]any{
		"kubeconfig": ir.EnvOverride{Var: EnvKubeconfig, Fallback: kubeconfig(clusterKey)},
	}, nil)
	r.Program.AddResource(provider)
	r.Program.Connect(providerKey, clusterKey, ir.EdgeDependsOn)
	r.Program.Connect(providerKey, nodesKey, ir.EdgeDependsOn)
}

// envAsK8sEnvVars renders a unit's environment as Kubernetes writes it.
//
// The same object the Fargate path uses, in the same shape, with one addition:
// an explicit cast. A container definition on ECS is serialised through
// jsonStringify and nothing checks its types; a Deployment's `env` is
// `Input<EnvVar>[]`, and Object.entries over a mixed object gives `unknown`
// values, which TypeScript refuses. The cast is safe because every binding is
// a string or an Output of one, and being made to say so is the k8s provider
// doing its job.
func envAsK8sEnvVars(unitID string) string {
	return "Object.entries(" + EnvConstName(unitID) +
		").map(([name, value]) => ({ name, value: value as pulumi.Input<string> }))"
}

// millicores renders a Fargate CPU number as Kubernetes writes CPU.
//
// Both count the same thing and neither says so in the same units: Fargate's
// 1024 is one vCPU, and Kubernetes writes one vCPU as "1000m". Dividing keeps
// the portable `memory:` meaning one thing across both platforms rather than
// two settings that happen to be near each other.
func millicores(fargateCPU string) string {
	n := 0
	for _, c := range fargateCPU {
		if c < '0' || c > '9' {
			return "250m"
		}
		n = n*10 + int(c-'0')
	}
	// 1024 Fargate units is 1 vCPU is 1000 millicores.
	return itoa(n*1000/1024) + "m"
}

// servicePrincipalPolicy is an assume-role policy for one AWS service.
func servicePrincipalPolicy(service string) ir.JSONDoc {
	return ir.JSONDoc{Value: map[string]any{
		"Version": "2012-10-17",
		"Statement": []any{map[string]any{
			"Effect":    "Allow",
			"Action":    "sts:AssumeRole",
			"Principal": map[string]any{"Service": service},
		}},
	}}
}

// isExposed reports whether anything fronts this unit.
func (r *Resolver) isExposed(unitID string) bool {
	for _, in := range r.Program.IntentsOfKind(config.KindExpose) {
		if e, ok := in.(*ir.Expose); ok && e.Unit == unitID {
			return true
		}
	}
	return false
}

// kubeconfig builds what the Kubernetes provider authenticates with, as an
// interpolation over the cluster's outputs.
//
// The same three parts `aws eks update-kubeconfig` writes: where the API server
// is, the certificate authority that signs it, and how to get a token. It has
// to be an interpolation rather than a string because two of the three do not
// exist until the cluster does.
//
// The exec plugin is what makes this work unchanged against an emulator --
// `aws eks get-token` is answered there too -- so nothing here knows or cares
// which it is talking to.
func kubeconfig(cluster ir.Key) ir.Interp {
	return ir.Lit(
		"apiVersion: v1\nclusters:\n- cluster:\n    server: ",
		ir.Ref{Key: cluster, Prop: "endpoint"},
		"\n    certificate-authority-data: ",
		ir.Ref{Key: cluster, Prop: "certificateAuthority.data"},
		"\n  name: cloudcc\ncontexts:\n- context:\n    cluster: cloudcc\n    user: cloudcc\n"+
			"  name: cloudcc\ncurrent-context: cloudcc\nkind: Config\nusers:\n- name: cloudcc\n"+
			"  user:\n    exec:\n      apiVersion: client.authentication.k8s.io/v1beta1\n"+
			"      command: aws\n      args: [\"eks\", \"get-token\", \"--cluster-name\", \"",
		ir.Ref{Key: cluster, Prop: "name"},
		"\"]\n",
	)
}
