package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cloudcompiler/cloudcc/internal/capabilities"
	"github.com/cloudcompiler/cloudcc/internal/compiler"
	"github.com/cloudcompiler/cloudcc/internal/config"
	"github.com/cloudcompiler/cloudcc/internal/diag"
	"github.com/spf13/afero"
	"github.com/spf13/cobra"
)

// StateFile records what the last compile produced, so `cloudcc deploy` can refuse
// to deploy output that no longer matches the source (D19).
const StateFile = ".cloudcc-state.json"

type compileOptions struct {
	app        string
	provider   string
	configPath string
	outDir     string
	strict     bool
	verbose    bool
	dumpIR     bool
	// quiet suppresses progress output, for the in-memory recompile that the
	// deploy staleness check runs.
	quiet bool
}

func newCompileCommand() *cobra.Command {
	var opts compileOptions
	cmd := &cobra.Command{
		Use:   "compile <path>",
		Short: "Compile an application into a Pulumi project",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "."
			if len(args) == 1 {
				path = args[0]
			} else if len(args) == 0 && !cmd.Flags().Changed("app") {
				// Compiling the working directory is a deliberate choice, not
				// an accident of leaving the path off.
				if _, err := os.Stat(config.DefaultFileName); err != nil {
					return usageErr("no path given and no %s in the working directory", config.DefaultFileName)
				}
			}
			_, err := runCompile(cmd, path, opts)
			return err
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.app, "app", "", "application name (overrides cloudcc.yaml)")
	f.StringVar(&opts.provider, "provider", "", "cloud provider (only \"aws\" is implemented)")
	f.StringVarP(&opts.configPath, "config", "c", "", "path to cloudcc.yaml")
	f.StringVarP(&opts.outDir, "out", "o", "", "output directory (default \"compiled\")")
	f.BoolVar(&opts.strict, "strict", false, "treat warnings as errors")
	f.BoolVarP(&opts.verbose, "verbose", "v", false, "log each compiler stage")
	f.BoolVar(&opts.dumpIR, "dump-ir", false, "print the intermediate representation as JSON")
	return cmd
}

// compileResult is what a successful compile produced.
type compileResult struct {
	Ctx    *compiler.Context
	OutDir string
}

// compileFingerprint compiles into memory and returns the fingerprint of the
// result. Nothing is written to disk, so it is safe to call before a deploy
// just to find out whether the existing output is current.
func compileFingerprint(cmd *cobra.Command, srcPath string, opts compileOptions) (string, error) {
	opts.quiet = true
	result, err := compileInto(cmd, srcPath, opts, afero.NewMemMapFs(), "")
	if err != nil {
		return "", err
	}
	return Fingerprint(result.Ctx)
}

func runCompile(cmd *cobra.Command, srcPath string, opts compileOptions) (*compileResult, error) {
	srcRoot, err := filepath.Abs(srcPath)
	if err != nil {
		return nil, usageErr("%v", err)
	}
	if fi, err := os.Stat(srcRoot); err != nil || !fi.IsDir() {
		return nil, usageErr("source path %s is not a directory", srcPath)
	}

	cfgPath := config.FindFile(opts.configPath, srcRoot)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, usageErr("%v", err)
	}
	if opts.app != "" {
		cfg.App = opts.app
	}
	if opts.provider != "" {
		cfg.Provider = opts.provider
	}
	if opts.outDir != "" {
		cfg.OutDir = opts.outDir
	}
	if cfg.App == "" {
		// A nameless app is almost always a forgotten flag rather than an
		// intent, so infer from the directory and say so.
		cfg.App = filepath.Base(srcRoot)
		fmt.Fprintf(cmd.ErrOrStderr(), "cloudcc: no app name configured; using %q\n", cfg.App)
	}
	if err := cfg.Validate(); err != nil {
		return nil, usageErr("%v", err)
	}

	outDir, err := filepath.Abs(cfg.AppOutDir())
	if err != nil {
		return nil, usageErr("%v", err)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, compileErr(err)
	}
	return compileInto(cmd, srcPath, opts, afero.NewBasePathFs(afero.NewOsFs(), outDir), outDir)
}

// compileInto runs the whole chain against a given output filesystem. outDir is
// the real path backing it, or "" for an in-memory run.
func compileInto(cmd *cobra.Command, srcPath string, opts compileOptions, out afero.Fs, outDir string) (*compileResult, error) {
	srcRoot, err := filepath.Abs(srcPath)
	if err != nil {
		return nil, usageErr("%v", err)
	}
	cfgPath := config.FindFile(opts.configPath, srcRoot)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, usageErr("%v", err)
	}
	if opts.app != "" {
		cfg.App = opts.app
	}
	if opts.provider != "" {
		cfg.Provider = opts.provider
	}
	if opts.outDir != "" {
		cfg.OutDir = opts.outDir
	}
	if cfg.App == "" {
		cfg.App = filepath.Base(srcRoot)
	}
	if err := cfg.Validate(); err != nil {
		return nil, usageErr("%v", err)
	}

	ctx := compiler.NewContext(cfg, srcRoot, out)
	ctx.OutDir = outDir
	if rel, err := filepath.Rel(srcRoot, mustAbs(cfgPath)); err == nil && !strings.HasPrefix(rel, "..") {
		ctx.ConfigPath = filepath.ToSlash(rel)
	}
	ctx.Diags.Strict = opts.strict
	if !opts.quiet {
		ctx.Notice = func(msg string) { fmt.Fprintf(cmd.ErrOrStderr(), "cloudcc: %s\n", msg) }
	}

	c, err := compiler.NewCompiler(capabilities.Chain())
	if err != nil {
		return nil, compileErr(err)
	}
	if opts.verbose {
		c.Trace = func(name string) { fmt.Fprintf(cmd.ErrOrStderr(), "cloudcc: %s\n", name) }
	}
	if err := c.Compile(ctx); err != nil {
		if !opts.quiet {
			reportDiagnostics(cmd, ctx.Diags)
		}
		return nil, compileErr(err)
	}
	if !opts.quiet {
		reportDiagnostics(cmd, ctx.Diags)
	}
	if ctx.Diags.HasErrors() {
		return nil, compileErr(fmt.Errorf("%d error(s); no output written", ctx.Diags.ErrorCount()))
	}

	if err := writeResolvedConfig(ctx); err != nil {
		return nil, compileErr(err)
	}
	if err := writeState(ctx); err != nil {
		return nil, compileErr(err)
	}
	if opts.quiet {
		return &compileResult{Ctx: ctx, OutDir: outDir}, nil
	}

	if opts.dumpIR {
		data, err := ctx.Graph.DumpJSON()
		if err != nil {
			return nil, compileErr(err)
		}
		cmd.OutOrStdout().Write(data)
	}

	fmt.Fprintf(cmd.ErrOrStderr(), "cloudcc: compiled %s into %s\n", cfg.App, cfg.AppOutDir())
	return &compileResult{Ctx: ctx, OutDir: outDir}, nil
}

// writeResolvedConfig records every decision the compile made (D5).
func writeResolvedConfig(ctx *compiler.Context) error {
	data, err := ctx.Config.ForOutput().Marshal()
	if err != nil {
		return err
	}
	header := "# Generated by cloudcc. This file records every decision the last compile made,\n" +
		"# including the defaults it filled in. Edit the cloudcc.yaml in your source tree,\n" +
		"# not this copy.\n"
	return afero.WriteFile(ctx.Out, config.DefaultFileName, append([]byte(header), data...), 0o644)
}

// State is the fingerprint record written next to the output.
type State struct {
	Version int    `json:"version"`
	App     string `json:"app"`
	// Fingerprint is the SHA-256 over the resolved config and the sorted IR,
	// which is what `cloudcc deploy` compares against a fresh compile (D19).
	Fingerprint string   `json:"fingerprint"`
	Units       []string `json:"units"`
}

func writeState(ctx *compiler.Context) error {
	fp, err := Fingerprint(ctx)
	if err != nil {
		return err
	}
	st := State{
		Version:     1,
		App:         ctx.Config.App,
		Fingerprint: fp,
		Units:       config.SortedKeys(ctx.UnitFiles),
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return afero.WriteFile(ctx.Out, StateFile, append(data, '\n'), 0o644)
}

// Fingerprint hashes the resolved configuration together with the IR dump.
// Both are byte-deterministic, so the same source and config always produce
// the same fingerprint.
func Fingerprint(ctx *compiler.Context) (string, error) {
	cfgData, err := ctx.Config.ForOutput().Marshal()
	if err != nil {
		return "", err
	}
	irData, err := ctx.Graph.DumpJSON()
	if err != nil {
		return "", err
	}
	var files []string
	for _, f := range ctx.Files.Files() {
		files = append(files, f.Path+":"+f.SHA256)
	}
	sort.Strings(files)
	joined := append(cfgData, irData...)
	joined = append(joined, []byte(strings.Join(files, "\n"))...)
	return hashHex(joined), nil
}

func mustAbs(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}

func reportDiagnostics(cmd *cobra.Command, d *diag.Diagnostics) {
	items := d.Items()
	if len(items) == 0 {
		return
	}
	w := cmd.ErrOrStderr()
	for _, it := range items {
		fmt.Fprintln(w, it.String())
	}
	if d.Truncated {
		fmt.Fprintf(w, "cloudcc: further diagnostics suppressed after %d\n", diag.MaxDiagnostics)
	}
}
