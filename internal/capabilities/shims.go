package capabilities

import (
	"github.com/cloudcompiler/cc/internal/compiler"
)

// ShimsPlugin rewrites SDK hint calls in the output copy into real cloud
// clients and writes the copy out (D13).
//
// Rewriting happens on a copy of the user's source; the input tree is never
// modified. Because the rewrite is a byte-range splice at each hint's recorded
// span, it depends on every capability plugin having already claimed its
// hints.
type ShimsPlugin struct{ base }

// NewShimsPlugin returns the shims stage.
func NewShimsPlugin() *ShimsPlugin {
	return &ShimsPlugin{base{name: PluginShims, deps: []string{PluginExecUnits, PluginStaticUnits}}}
}

func (p *ShimsPlugin) Transform(ctx *compiler.Context) error {
	return writeUnitTrees(ctx)
}
