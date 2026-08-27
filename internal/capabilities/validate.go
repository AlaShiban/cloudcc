package capabilities

import (
	"fmt"
	"sort"
	"strings"

	"github.com/cloudcompiler/cloudcc/internal/compiler"
	"github.com/cloudcompiler/cloudcc/internal/config"
	"github.com/cloudcompiler/cloudcc/internal/diag"
	"github.com/cloudcompiler/cloudcc/internal/ir"
	"github.com/cloudcompiler/cloudcc/internal/provider/aws"
)

// ValidatePlugin checks every resolved type against the provider's allow-list
// before anything is generated.
//
// Types that the schema accepts but the provider has not implemented -- eks,
// cockroachdb_serverless -- are rejected here with a plain "not yet supported"
// message rather than being silently swapped for something else.
type ValidatePlugin struct{ base }

// NewValidatePlugin returns the validate stage.
func NewValidatePlugin() *ValidatePlugin {
	return &ValidatePlugin{base{name: PluginValidate, deps: []string{
		PluginExecUnits, PluginExpose, PluginPersist, PluginPubSub, PluginConfigVars,
	}}}
}

func (p *ValidatePlugin) Transform(ctx *compiler.Context) error {
	// Logging is checked separately because it is the one kind declared only in
	// configuration: there is no intent to walk, so nothing below would ever
	// look at it, and an unrecognised destination would be silently dropped --
	// leaving an application that looks configured and is not.
	p.validateLogging(ctx)
	p.warnKubernetesHasNoIdentity(ctx)

	for _, in := range ctx.Graph.Intents() {
		kind := in.Capability()
		typ := in.Config().Type
		if typ == "" {
			continue
		}
		switch aws.Support(kind, typ) {
		case aws.Supported:
			// nothing to say
		case aws.NotYetSupported:
			ctx.Diags.Errorf(diag.Position{}, kind,
				"%q is not yet supported for %s (declared for %q)%s",
				typ, kind, in.Key().ID, why(in))
		default:
			ctx.Diags.Errorf(diag.Position{}, kind,
				"unknown type %q for %s (declared for %q); supported types are %s",
				typ, kind, in.Key().ID, strings.Join(aws.SupportedTypes(kind), ", "))
		}
	}
	return nil
}

// why explains a type the program did not name. A topic's backing is chosen
// from its requirements, so "kinesis is not yet supported" on its own would
// leave the author looking for a word that appears nowhere in their code.
func why(in ir.Intent) string {
	topic, ok := in.(*ir.Topic)
	if !ok || topic.Because == "" {
		return ""
	}
	return ". cloudcc chose it because " + topic.Because
}

func (p *ValidatePlugin) validateLogging(ctx *compiler.Context) {
	dest := ctx.Config.LogDestination()
	switch aws.Support(config.KindLogging, dest.Type) {
	case aws.Supported:
	case aws.NotYetSupported:
		ctx.Diags.Errorf(diag.Position{}, config.KindLogging,
			"logging.type %q is not yet supported; %s is the only destination "+
				"implemented. The seam for a vendor is the destination, not your "+
				"call sites -- nothing in the application changes when this does",
			dest.Type, strings.Join(aws.SupportedTypes(config.KindLogging), ", "))
	default:
		ctx.Diags.Errorf(diag.Position{}, config.KindLogging,
			"unknown logging.type %q; supported destinations are %s",
			dest.Type, strings.Join(aws.SupportedTypes(config.KindLogging), ", "))
	}
	if dest.RetentionDays < 0 {
		ctx.Diags.Errorf(diag.Position{}, config.KindLogging,
			"logging.retention_days is %d; it must be positive", dest.RetentionDays)
	}
}

// DescribeSupport renders the provider's type matrix, used by documentation
// and by the error path above.
func DescribeSupport() string {
	var b strings.Builder
	kinds := append([]string(nil), config.Kinds...)
	sort.Strings(kinds)
	for _, kind := range kinds {
		types := aws.SupportedTypes(kind)
		if len(types) == 0 {
			continue
		}
		fmt.Fprintf(&b, "%-16s %s\n", kind, strings.Join(types, ", "))
	}
	return b.String()
}

// warnKubernetesHasNoIdentity reports a Kubernetes unit that reaches an AWS
// resource it will have no permission to use.
//
// Every other execution unit's permissions are derived from what its code
// bundles: a Lambda gets a role, a Fargate task gets a task role, and least
// privilege is a consequence of the import graph rather than a list somebody
// maintains. A pod's equivalent is IRSA -- an OIDC provider on the cluster, a
// role that trusts it, and a ServiceAccount annotated with that role -- and
// none of it is emitted yet.
//
// So the bindings reach the container and the permission to use them does not.
// Against an emulator that makes no difference, because it authenticates
// nothing; against AWS the first call fails with AccessDenied. Saying so at
// compile time is the difference between a known gap and a deployment that
// looks finished.
func (p *ValidatePlugin) warnKubernetesHasNoIdentity(ctx *compiler.Context) {
	for _, in := range ctx.Graph.IntentsOfKind(config.KindExecutionUnit) {
		u, ok := in.(*ir.ExecUnit)
		if !ok || u.Config().Platform != config.PlatformKubernetes {
			continue
		}
		var reaches []string
		for _, e := range ctx.Graph.EdgesFrom(u.Key(), ir.EdgeUses) {
			if strings.HasPrefix(e.To.Kind, "persist_") {
				reaches = append(reaches, e.To.ID)
			}
		}
		if len(reaches) == 0 {
			continue
		}
		sort.Strings(reaches)
		ctx.Diags.Warnf(diag.Position{}, config.KindExecutionUnit,
			"unit %q runs on Kubernetes and reaches %s, but a pod gets no AWS identity yet: "+
				"IRSA -- an OIDC provider, a role trusting it, and an annotated ServiceAccount "+
				"-- is not emitted. The bindings reach the container; permission to use them "+
				"does not, so the first call fails against real AWS and succeeds against an "+
				"emulator that authenticates nothing",
			u.ID, strings.Join(reaches, ", "))
	}
}
