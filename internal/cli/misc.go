package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/cloudcompiler/cloudcc/internal/config"
	"github.com/spf13/cobra"
)

// Version is stamped at build time.
var Version = "dev"

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the cloudcc version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintf(cmd.OutOrStdout(), "cloudcc %s\n", Version)
			return nil
		},
	}
}

const initTemplate = `# CloudCompiler configuration.
#
# Every type-selection decision the compiler makes is driven from this file.
# Anything you leave out is filled in from the provider defaults, and the
# fully-resolved result is written to %s/cloudcc.yaml after each compile.

app: %s
provider: aws
out_dir: %s

# Uncomment to change a provider default for a whole capability kind.
# defaults:
#   execution_unit:
#     type: function      # function | container  (lambda | ecs also accepted)
#   persist_redis:
#     type: elasticache   # elasticache | memorydb

# Uncomment to configure one resource by id.
#
# "type" is what a unit is; "platform" is where it runs. Both are portable, so
# moving a container from Fargate to Kubernetes is this file's business and not
# your program's.
# execution_units:
#   api:
#     type: function
#     memory: 512
#     environment_variables:
#       LOG_LEVEL: info
#   worker:
#     type: container
#     platform: kubernetes   # serverless | kubernetes
#
# persisted:
#   petsByOwner:
#     type: dynamodb
#     pulumi_params:
#       billingMode: PAY_PER_REQUEST
#
# static_units:
#   site:
#     type: cloudfront    # s3 | cloudfront
#
# logging:
#   type: cloudwatch
#   retention_days: 14
`

func newInitCommand() *cobra.Command {
	var app string
	var force bool
	cmd := &cobra.Command{
		Use:   "init [path]",
		Short: "Scaffold a cloudcc.yaml",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			abs, err := filepath.Abs(dir)
			if err != nil {
				return usageErr("%v", err)
			}
			if app == "" {
				app = filepath.Base(abs)
			}
			if err := config.ValidateAppName(app); err != nil {
				return usageErr("%v", err)
			}
			target := filepath.Join(abs, config.DefaultFileName)
			if _, err := os.Stat(target); err == nil && !force {
				return usageErr("%s already exists; pass --force to overwrite it", target)
			}
			body := fmt.Sprintf(initTemplate, config.DefaultOutDir, app, config.DefaultOutDir)
			if err := os.WriteFile(target, []byte(body), 0o644); err != nil {
				return exitError{ExitCompile, err}
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "cloudcc: wrote %s\n", target)
			return nil
		},
	}
	cmd.Flags().StringVar(&app, "app", "", "application name (defaults to the directory name)")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing cloudcc.yaml")
	return cmd
}

func hashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
