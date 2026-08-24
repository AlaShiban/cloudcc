// Package capabilities holds the compiler's plugin chain: the stages that turn
// detected SDK hints into intent nodes and edges in the shared IR.
//
// Only plugins in this package create intents. Concrete provider resources are
// created exclusively by internal/provider/aws (D7).
package capabilities

import "github.com/cloudcompiler/cc/internal/compiler"

// Plugin names. Declaring them as constants keeps the dependency declarations
// below honest -- a typo becomes a build-time schedule error rather than a
// silent reordering.
const (
	PluginConfig      = "config"
	PluginInput       = "input"
	PluginDetect      = "detect"
	PluginStaticUnits = "static-units"
	PluginEmbedAssets = "embed-assets"
	PluginExecUnits   = "exec-units"
	PluginExpose      = "expose"
	PluginPersist     = "persist"
	PluginPubSub      = "pubsub"
	PluginConfigVars  = "config-vars"
	PluginValidate    = "validate"
	PluginShims       = "shims"
	PluginResolveAWS  = "resolve:aws"
	PluginTopology    = "topology"
	PluginRender      = "render:pulumi-ts"
)

// base supplies Name and Deps so each plugin only has to write Transform.
type base struct {
	name string
	deps []string
}

func (b base) Name() string   { return b.name }
func (b base) Deps() []string { return b.deps }

var _ compiler.Plugin = struct {
	base
	transformer
}{}

type transformer interface {
	Transform(*compiler.Context) error
}
