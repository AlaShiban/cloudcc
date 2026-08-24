package capabilities

import "github.com/cloudcompiler/cc/internal/compiler"

// Chain returns every compiler plugin. Order is irrelevant here: the scheduler
// derives it from each plugin's declared dependencies (D6).
func Chain(extra ...compiler.Plugin) []compiler.Plugin {
	plugins := []compiler.Plugin{
		NewConfigPlugin(),
		NewInputPlugin(),
		NewDetectPlugin(),
		NewStaticUnitsPlugin(),
		NewExecUnitsPlugin(),
	}
	return append(plugins, extra...)
}
