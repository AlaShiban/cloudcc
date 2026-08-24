package aws

import (
	"github.com/cloudcompiler/cc/internal/ir"
	"github.com/cloudcompiler/cc/internal/sanitize"
)

// VPCID is the id every network resource shares, since one application gets
// one VPC.
const VPCID = "network"

// subnetCount is how many public subnets the generated VPC gets. Two is the
// minimum an ALB will accept, and enough for a Fargate service to survive one
// availability zone going away.
const subnetCount = 2

// vpcNeeded reports whether anything in the program requires a VPC.
//
// Lambda deliberately does not: putting a function in a VPC costs cold-start
// time and buys nothing unless it must reach a private resource. Only the
// types that genuinely cannot exist outside one pull a VPC in.
func (r *Resolver) vpcNeeded() bool {
	var types []string
	for _, in := range r.Program.Intents() {
		types = append(types, in.Config().Type)
	}
	return NeedsVPC(types)
}

// network creates the VPC and its public subnets, once, on demand.
func (r *Resolver) network() {
	vpcKey := ir.Key{Kind: KindVPC, ID: VPCID}
	if _, done := r.Program.Resource(vpcKey); done {
		return
	}

	zones := ir.NewResource(KindAvailZones, VPCID, "aws.getAvailabilityZones", map[string]any{
		"state": "available",
	}, nil)
	r.Program.AddResource(zones)

	vpc := ir.NewResource(KindVPC, VPCID, "aws.ec2.Vpc", map[string]any{
		"cidrBlock":          "10.0.0.0/16",
		"enableDnsHostnames": true,
		"enableDnsSupport":   true,
		"tags":               map[string]any{"Name": sanitize.IAMName(r.App, VPCID, "")},
	}, nil)
	r.Program.AddResource(vpc)

	gw := ir.NewResource(KindInternetGW, VPCID, "aws.ec2.InternetGateway", map[string]any{
		"vpcId": ir.Ref{Key: vpcKey, Prop: "id"},
	}, nil)
	r.Program.AddResource(gw)

	routes := ir.NewResource(KindRouteTable, VPCID, "aws.ec2.RouteTable", map[string]any{
		"vpcId": ir.Ref{Key: vpcKey, Prop: "id"},
		"routes": []any{map[string]any{
			"cidrBlock": "0.0.0.0/0",
			"gatewayId": ir.Ref{Key: gw.Key(), Prop: "id"},
		}},
	}, nil)
	r.Program.AddResource(routes)

	for i := 0; i < subnetCount; i++ {
		id := subnetID(i)
		subnet := ir.NewResource(KindSubnet, id, "aws.ec2.Subnet", map[string]any{
			"vpcId":               ir.Ref{Key: vpcKey, Prop: "id"},
			"cidrBlock":           cidrForSubnet(i),
			"availabilityZone":    ir.Ref{Key: zones.Key(), Prop: "names.apply(n => n[" + itoa(i) + "])"},
			"mapPublicIpOnLaunch": true,
			"tags":                map[string]any{"Name": sanitize.IAMName(r.App, id, "")},
		}, nil)
		r.Program.AddResource(subnet)

		assoc := ir.NewResource(KindRouteAssoc, id, "aws.ec2.RouteTableAssociation", map[string]any{
			"subnetId":     ir.Ref{Key: subnet.Key(), Prop: "id"},
			"routeTableId": ir.Ref{Key: routes.Key(), Prop: "id"},
		}, nil)
		r.Program.AddResource(assoc)
	}

	// One security group, open outbound and open inbound within the VPC, is
	// enough for a compiled application: the load balancer is what faces the
	// internet, and datastores only ever see traffic from these subnets.
	sg := ir.NewResource(KindSecurityGroup, VPCID, "aws.ec2.SecurityGroup", map[string]any{
		"vpcId":       ir.Ref{Key: vpcKey, Prop: "id"},
		"description": "Managed by cc: traffic between compiled units and their datastores",
		"ingress": []any{map[string]any{
			"protocol":    "-1",
			"fromPort":    0,
			"toPort":      0,
			"cidrBlocks":  []any{"0.0.0.0/0"},
			"description": "inbound from the load balancer and within the VPC",
		}},
		"egress": []any{map[string]any{
			"protocol":   "-1",
			"fromPort":   0,
			"toPort":     0,
			"cidrBlocks": []any{"0.0.0.0/0"},
		}},
	}, nil)
	r.Program.AddResource(sg)
}

// subnetKeys returns the generated subnet keys in order.
func subnetKeys() []ir.Key {
	out := make([]ir.Key, 0, subnetCount)
	for i := 0; i < subnetCount; i++ {
		out = append(out, ir.Key{Kind: KindSubnet, ID: subnetID(i)})
	}
	return out
}

// subnetIDList renders the subnet ids as a property value.
func subnetIDList() []any {
	out := make([]any, 0, subnetCount)
	for _, key := range subnetKeys() {
		out = append(out, ir.Ref{Key: key, Prop: "id"})
	}
	return out
}

func securityGroupKey() ir.Key { return ir.Key{Kind: KindSecurityGroup, ID: VPCID} }
func vpcKey() ir.Key           { return ir.Key{Kind: KindVPC, ID: VPCID} }

func subnetID(i int) string { return VPCID + "-" + itoa(i) }

// cidrForSubnet carves /24s out of the VPC range, one per subnet.
func cidrForSubnet(i int) string { return "10.0." + itoa(i) + ".0/24" }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		return "-" + string(digits)
	}
	return string(digits)
}

// subnetGroup creates the per-service subnet group a managed datastore needs
// to live in the generated VPC, once per template.
func (r *Resolver) subnetGroup(id, template string) ir.Key {
	key := ir.Key{Kind: KindSubnetGroup, ID: id}
	if _, done := r.Program.Resource(key); done {
		return key
	}
	r.network()
	r.Program.AddResource(ir.NewResource(KindSubnetGroup, id, template, map[string]any{
		"name":      sanitize.ElastiCacheCluster(r.App, id),
		"subnetIds": subnetIDList(),
	}, nil))
	return key
}
