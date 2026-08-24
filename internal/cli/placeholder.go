package cli

import (
	"fmt"

	"github.com/cloudcompiler/cc/internal/topology"
	"github.com/spf13/cobra"
)

// notYet reports a command that exists in the CLI surface but lands in a later
// milestone. It is a clean error rather than a silent no-op.
func notYet(name, milestone string) error {
	return exitError{ExitUsage, fmt.Errorf("`cc %s` is not implemented yet (lands in %s)", name, milestone)}
}

func newDeployCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Deploy the compiled project",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return notYet("deploy", "M4")
		},
	}
	return cmd
}

func newDiagramCommand() *cobra.Command {
	var opts compileOptions
	var toStdout string
	cmd := &cobra.Command{
		Use:   "diagram [path]",
		Short: "Render the architecture topology",
		Long: "Compiles the application and writes topology.mmd and topology.dot next to\n" +
			"the generated project, plus a PNG when graphviz is installed. Nothing is\n" +
			"sent anywhere: rendering is entirely local.",
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
			switch toStdout {
			case "":
				fmt.Fprintf(cmd.ErrOrStderr(), "cc: wrote %s and %s to %s\n",
					topology.MermaidFile, topology.DOTFile, result.Ctx.Config.OutDir)
			case "mermaid":
				cmd.OutOrStdout().Write(topology.Mermaid(result.Ctx.Graph, topology.Options{
					App: result.Ctx.Config.App, View: topology.Intents,
				}))
			case "dot":
				cmd.OutOrStdout().Write(topology.DOT(result.Ctx.Graph, topology.Options{
					App: result.Ctx.Config.App, View: topology.Intents,
				}))
			default:
				return usageErr("unknown --format %q; use mermaid or dot", toStdout)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.app, "app", "", "application name (overrides cc.yaml)")
	f.StringVarP(&opts.configPath, "config", "c", "", "path to cc.yaml")
	f.StringVarP(&opts.outDir, "out", "o", "", "output directory (default \"compiled\")")
	f.StringVar(&toStdout, "format", "", "also print the diagram to stdout: mermaid or dot")
	return cmd
}
