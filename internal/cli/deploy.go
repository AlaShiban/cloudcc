package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cloudcompiler/cloudcc/internal/config"
	"github.com/cloudcompiler/cloudcc/internal/deploy"
	"github.com/spf13/cobra"
)

func newDeployCommand() *cobra.Command {
	var (
		stack      string
		preview    bool
		destroy    bool
		force      bool
		skipPkg    bool
		outDir     string
		configPath string
		appName    string
		sourcePath string
		region     string
	)

	cmd := &cobra.Command{
		Use:   "deploy [path]",
		Short: "Deploy the compiled project",
		Long: "Deploys the project cloudcc generated, via the Pulumi Automation API.\n\n" +
			"Before touching anything, cloudcc recompiles the source in memory and compares\n" +
			"its fingerprint with the one recorded in the output. Deploying output that\n" +
			"no longer matches your source is refused unless you pass --force.\n\n" +
			"Use --stack " + deploy.MinistackStack + " to deploy against a local AWS emulator instead of\n" +
			"real AWS; cloudcc configures the endpoints for you.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if preview && destroy {
				return usageErr("--preview and --destroy are mutually exclusive")
			}
			if len(args) == 1 {
				sourcePath = args[0]
			}

			cfg, err := loadDeployConfig(sourcePath, configPath, appName, outDir)
			if err != nil {
				return err
			}
			absOut, err := filepath.Abs(cfg.AppOutDir())
			if err != nil {
				return usageErr("%v", err)
			}

			action := deploy.ActionUp
			switch {
			case preview:
				action = deploy.ActionPreview
			case destroy:
				action = deploy.ActionDestroy
			}

			stackName := stack
			if stackName == "" {
				stackName = cfg.App
			}
			emulator := ""
			if strings.EqualFold(stackName, deploy.MinistackStack) {
				emulator = MinistackEndpoint()
			}

			// Destroying does not depend on the output matching the source --
			// it only needs the stack -- so the fingerprint check is skipped.
			fingerprint := ""
			if action != deploy.ActionDestroy {
				fingerprint, err = currentFingerprint(cmd, sourcePath, configPath, appName, outDir)
				if err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "cloudcc: could not recompile to check the output is current: %v\n", err)
				}
			}

			warnings, perr := deploy.Preflight(deploy.PreflightInput{
				Dir:                absOut,
				CurrentFingerprint: fingerprint,
				RequireCredentials: emulator == "" && action != deploy.ActionDestroy,
				Force:              force,
			})
			for _, w := range warnings {
				fmt.Fprintf(cmd.ErrOrStderr(), "cloudcc: %s\n", w)
			}
			if perr != nil {
				return exitError{ExitCompile, perr}
			}

			fmt.Fprintf(cmd.ErrOrStderr(), "cloudcc: %s %s\n", action, deploy.DescribeStack(stackName, emulator))

			return runDeploy(cmd, deploy.Options{
				Dir:              absOut,
				App:              cfg.App,
				Stack:            stackName,
				Action:           action,
				Force:            force,
				EmulatorEndpoint: emulator,
				Region:           region,
				Out:              cmd.OutOrStdout(),
				Err:              cmd.ErrOrStderr(),
				SkipPackaging:    skipPkg,
			})
		},
	}

	f := cmd.Flags()
	f.StringVar(&stack, "stack", "", "stack name (defaults to the app name; \""+deploy.MinistackStack+"\" targets a local emulator)")
	f.BoolVar(&preview, "preview", false, "show what would change without changing it")
	f.BoolVar(&destroy, "destroy", false, "remove everything the stack created")
	f.BoolVar(&force, "force", false, "deploy even if the output does not match the source")
	f.BoolVar(&skipPkg, "skip-packaging", false, "do not run the generated packaging scripts")
	f.StringVarP(&outDir, "out", "o", "", "compiled output directory (default \"compiled\")")
	f.StringVarP(&configPath, "config", "c", "", "path to cloudcc.yaml")
	f.StringVar(&appName, "app", "", "application name (overrides cloudcc.yaml)")
	f.StringVar(&region, "region", "us-east-1", "AWS region")
	return cmd
}

// loadDeployConfig resolves just enough configuration to find the output
// directory and the app name.
func loadDeployConfig(sourcePath, configPath, appName, outDir string) (*config.App, error) {
	if sourcePath == "" {
		sourcePath = "."
	}
	srcRoot, err := filepath.Abs(sourcePath)
	if err != nil {
		return nil, usageErr("%v", err)
	}
	cfg, err := config.Load(config.FindFile(configPath, srcRoot))
	if err != nil {
		return nil, usageErr("%v", err)
	}
	if appName != "" {
		cfg.App = appName
	}
	if outDir != "" {
		cfg.OutDir = outDir
	}
	if cfg.App == "" {
		// Fall back to the name the last compile recorded.
		// The app name is what decides the output folder, so it cannot be read
		// from inside one. Look for a state file in each child of out_dir; a
		// single match is unambiguous, and more than one means the user has to
		// say which app they meant.
		if abs, aerr := filepath.Abs(cfg.OutDir); aerr == nil {
			if entries, rerr := os.ReadDir(abs); rerr == nil {
				var found []string
				for _, e := range entries {
					if !e.IsDir() {
						continue
					}
					if st, serr := deploy.ReadState(filepath.Join(abs, e.Name())); serr == nil && st.App != "" {
						found = append(found, st.App)
					}
				}
				if len(found) == 1 {
					cfg.App = found[0]
				} else if len(found) > 1 {
					sort.Strings(found)
					return nil, usageErr(
						"%s holds several compiled applications (%s); say which one with --app",
						cfg.OutDir, strings.Join(found, ", "))
				}
			}
		}
	}
	if cfg.App == "" {
		cfg.App = filepath.Base(srcRoot)
	}
	return cfg, nil
}

// currentFingerprint recompiles the source into a throwaway in-memory output
// and returns its fingerprint, which is what the staleness check compares
// against (D19).
func currentFingerprint(cmd *cobra.Command, sourcePath, configPath, appName, outDir string) (string, error) {
	if sourcePath == "" {
		sourcePath = "."
	}
	if _, err := os.Stat(sourcePath); err != nil {
		return "", fmt.Errorf("no source at %s", sourcePath)
	}
	return compileFingerprint(cmd, sourcePath, compileOptions{
		app:        appName,
		configPath: configPath,
		outDir:     outDir,
	})
}

// runDeploy executes the deploy and turns its error into a clean exit code.
func runDeploy(cmd *cobra.Command, opts deploy.Options) error {
	if err := deploy.Run(cmd.Context(), opts); err != nil {
		return exitError{ExitCompile, err}
	}
	return nil
}
