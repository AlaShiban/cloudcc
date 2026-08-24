package cli

import (
	"fmt"

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
	return &cobra.Command{
		Use:   "diagram",
		Short: "Render the architecture topology",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return notYet("diagram", "M5")
		},
	}
}
