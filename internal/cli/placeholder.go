package cli

import (
	"fmt"

	"github.com/cloudcompiler/cloudcc/internal/topology"
	"github.com/spf13/cobra"
)

func newDiagramCommand() *cobra.Command {
	var opts compileOptions
	var toStdout string
	var view string
	cmd := &cobra.Command{
		Use:   "diagram [path]",
		Short: "Render the architecture diagrams",
		Long: "Compiles the application and writes two diagrams next to the generated\n" +
			"project, plus PNGs when graphviz is installed:\n\n" +
			"  topology.mmd/.dot      what the program declared -- the capability layer\n" +
			"  architecture.mmd/.dot  what it compiled to -- every resource that will exist\n\n" +
			"Both are written by every compile; this command only re-runs one and can\n" +
			"print it. Nothing is sent anywhere: rendering is entirely local.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "."
			if len(args) == 1 {
				path = args[0]
			}
			result, err := runCompile(cmd, path, opts)
			if err != nil {
				return err
			}
			var chosen topology.View
			switch view {
			case "topology", "intents":
				chosen = topology.Intents
			case "architecture", "resources":
				chosen = topology.Resources
			default:
				return usageErr("unknown --view %q; use topology or architecture", view)
			}
			mermaidFile, dotFile := chosen.Files()
			opts := topology.Options{App: result.Ctx.Config.App, View: chosen}

			switch toStdout {
			case "":
				fmt.Fprintf(cmd.ErrOrStderr(), "cloudcc: wrote %s and %s to %s\n",
					mermaidFile, dotFile, result.Ctx.Config.AppOutDir())
			case "mermaid":
				cmd.OutOrStdout().Write(topology.Mermaid(result.Ctx.Graph, opts))
			case "dot":
				cmd.OutOrStdout().Write(topology.DOT(result.Ctx.Graph, opts))
			default:
				return usageErr("unknown --format %q; use mermaid or dot", toStdout)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&view, "view", "topology", "which layer to draw: topology or architecture")
	f.StringVar(&opts.app, "app", "", "application name (overrides cloudcc.yaml)")
	f.StringVarP(&opts.configPath, "config", "c", "", "path to cloudcc.yaml")
	f.StringVarP(&opts.outDir, "out", "o", "", "output directory (default \"compiled\")")
	f.StringVar(&toStdout, "format", "", "also print the diagram to stdout: mermaid or dot")
	return cmd
}
