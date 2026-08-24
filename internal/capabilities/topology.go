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
	result, err := topology.Write(ctx.Graph, ctx.Out, ctx.OutDir, topology.Options{
		App:  ctx.Config.App,
		View: topology.Intents,
	})
	if err != nil {
		return err
	}
	if result.Notice != "" && ctx.Notice != nil {
		ctx.Notice(fmt.Sprintf("topology: %s", result.Notice))
	}
	return nil
}
