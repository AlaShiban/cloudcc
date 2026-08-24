// Package cli wires the cobra command tree.
package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Exit codes.
const (
	ExitOK      = 0
	ExitCompile = 1
	ExitUsage   = 2
)

// exitError carries an exit code out of a command.
type exitError struct {
	code int
	err  error
}

func (e exitError) Error() string { return e.err.Error() }
func (e exitError) Unwrap() error { return e.err }

func usageErr(format string, args ...any) error {
	return exitError{ExitUsage, fmt.Errorf(format, args...)}
}

func compileErr(err error) error { return exitError{ExitCompile, err} }

// NewRootCommand builds the command tree. `cc <path>` compiles, so compile is
// both a named subcommand and the default action.
func NewRootCommand() *cobra.Command {
	compile := newCompileCommand()

	root := &cobra.Command{
		Use:   "cc [path]",
		Short: "CloudCompiler: compile a Python app into cloud infrastructure",
		Long: "CloudCompiler reads a plain Python application that uses the cloudcompiler\n" +
			"SDK for hints, and emits a runnable Pulumi project alongside a copy of the\n" +
			"application wired to real cloud services.\n\n" +
			"Running `cc ./app` is the same as running `cc compile ./app`.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ArbitraryArgs,
		RunE:          compile.RunE,
	}
	// The default action shares compile's flags so `cc ./app -o out` works.
	root.Flags().AddFlagSet(compile.Flags())

	root.AddCommand(compile)
	root.AddCommand(newDeployCommand())
	root.AddCommand(newDiagramCommand())
	root.AddCommand(newInitCommand())
	root.AddCommand(newDoctorCommand())
	root.AddCommand(newVersionCommand())
	return root
}

// Execute runs the CLI and returns the process exit code.
func Execute(args []string) int {
	root := NewRootCommand()
	root.SetArgs(args)
	return execute(root)
}

// execute runs a prepared command tree, reporting failures through the
// command's own error stream so tests observe exactly what a user would.
func execute(root *cobra.Command) int {
	err := root.Execute()
	if err == nil {
		return ExitOK
	}
	var ee exitError
	code := ExitUsage
	if asExit(err, &ee) {
		code = ee.code
		err = ee.err
	}
	fmt.Fprintf(root.ErrOrStderr(), "cc: %v\n", err)
	return code
}

func asExit(err error, target *exitError) bool {
	for err != nil {
		if e, ok := err.(exitError); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
