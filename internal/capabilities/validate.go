package capabilities

import (
	"fmt"
	"sort"
	"strings"

	"github.com/cloudcompiler/cloudcc/internal/compiler"
	"github.com/cloudcompiler/cloudcc/internal/config"
	"github.com/cloudcompiler/cloudcc/internal/diag"
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
				"%q is not yet supported for %s (declared for %q)", typ, kind, in.Key().ID)
		default:
			ctx.Diags.Errorf(diag.Position{}, kind,
				"unknown type %q for %s (declared for %q); supported types are %s",
				typ, kind, in.Key().ID, strings.Join(aws.SupportedTypes(kind), ", "))
		}
	}
	return nil
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
