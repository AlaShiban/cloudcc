package capabilities

import (
	"fmt"

	"github.com/cloudcompiler/cloudcc/internal/compiler"
	"github.com/cloudcompiler/cloudcc/internal/topology"
)

// TopologyPlugin writes the architecture diagram alongside the generated
// project. It runs after resolution so both IR layers exist, and it never
// touches the network (D12).
type TopologyPlugin struct{ base }

// NewTopologyPlugin returns the topology stage.
func NewTopologyPlugin() *TopologyPlugin {
	return &TopologyPlugin{base{name: PluginTopology, deps: []string{PluginResolveAWS}}}
}

func (p *TopologyPlugin) Transform(ctx *compiler.Context) error {
	if ctx.Failed() {
		return nil
	}
	// Both layers, every time. They answer different questions: the intent
	// diagram is what the program declared and is the one to reason about; the
	// architecture diagram is every resource that will exist in the account,
	// and is the one to review before a deploy.
	//
	// Writing both unconditionally rather than behind a flag is deliberate. A
	// picture of the compiled architecture that has to be asked for is a
	// picture nobody has when they need it -- and it costs two small text
	// files, rendered from the program's own edges, so it cannot fall out of
	// step with what was compiled.
	for _, view := range []topology.View{topology.Intents, topology.Resources} {
		result, err := topology.Write(ctx.Graph, ctx.Out, ctx.OutDir, topology.Options{
			App:  ctx.Config.App,
			View: view,
		})
		if err != nil {
			return err
		}
		if result.Notice != "" && ctx.Notice != nil {
			ctx.Notice(fmt.Sprintf("%s: %s", view.Name(), result.Notice))
		}
	}
	return nil
}
