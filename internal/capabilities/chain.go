package capabilities

import (
	"github.com/cloudcompiler/cloudcc/internal/compiler"
	"github.com/cloudcompiler/cloudcc/internal/iac/pulumi_ts"

	// Language frontends register themselves. Importing them here, where the
	// chain is assembled, is what makes a language available.
	_ "github.com/cloudcompiler/cloudcc/internal/lang/node"
	_ "github.com/cloudcompiler/cloudcc/internal/lang/python"
)

// IntentChain returns the stages that read source and build the intent layer.
// Nothing here creates a concrete resource; that is the resolver's job alone
// (D7).
func IntentChain() []compiler.Plugin {
	return []compiler.Plugin{
		NewConfigPlugin(),
		NewInputPlugin(),
		NewDetectPlugin(),
		NewStaticUnitsPlugin(),
		NewEmbedAssetsPlugin(),
		NewExecUnitsPlugin(),
		NewExposePlugin(),
		NewPersistPlugin(),
		NewPubSubPlugin(),
		NewConfigVarsPlugin(),
		NewValidatePlugin(),
	}
}

// Chain returns every compiler plugin. Order is irrelevant here: the scheduler
// derives it from each plugin's declared dependencies (D6).
func Chain(extra ...compiler.Plugin) []compiler.Plugin {
	plugins := append(IntentChain(),
		NewShimsPlugin(),
		NewResolveAWSPlugin(),
		NewRenderPlugin(pulumi_ts.BackendName),
		NewTopologyPlugin(),
	)
	return append(plugins, extra...)
}
