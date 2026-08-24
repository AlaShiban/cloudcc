// Package compiler defines the plugin contract, the shared compilation
// context, and the dependency scheduler.
//
// Klotho 1 ordered its plugins by hand, which made the ordering load-bearing
// and invisible. Here every plugin declares the plugins it depends on by name
// and the schedule is derived (D6). Execution stays sequential in topological
// order so plugins can mutate the shared context without synchronisation; the
// DAG leaves level-parallelism available later.
package compiler

import (
	"fmt"

	"github.com/cloudcompiler/cloudcc/internal/config"
	"github.com/cloudcompiler/cloudcc/internal/diag"
	"github.com/cloudcompiler/cloudcc/internal/graph"
	"github.com/cloudcompiler/cloudcc/internal/ir"
	"github.com/cloudcompiler/cloudcc/internal/sdkdetect"
	"github.com/cloudcompiler/cloudcc/internal/source"
	"github.com/spf13/afero"
)

// Plugin is one compilation stage.
type Plugin interface {
	// Name is the identifier other plugins declare in Deps.
	Name() string
	// Deps names the plugins that must run before this one.
	Deps() []string
	// Transform mutates the shared context.
	Transform(*Context) error
}

// Context is the state every plugin reads and writes.
type Context struct {
	// Config is the layered configuration, updated in place as decisions are
	// resolved so that the emitted cloudcc.yaml records them (D5).
	Config *config.App
	// Files is the working copy of the input tree. Plugins mutate this set,
	// never the user's directory (D13).
	Files *source.Set
	// Graph is the two-layer IR.
	Graph *ir.Program
	// Diags accumulates warnings and errors.
	Diags *diag.Diagnostics
	// Out is the output directory, as a filesystem so tests can run in memory.
	Out afero.Fs

	// SrcRoot is the absolute path of the user's source directory, kept for
	// diagnostics and for reading files the walker deliberately skipped.
	SrcRoot string
	// OutDir is the output directory path on the real filesystem, or "" when
	// Out is in-memory.
	OutDir string
	// ConfigPath is the root-relative path of the cloudcc.yaml that was loaded, when
	// it lives inside the source tree. It is excluded from unit bundles: the
	// resolved copy at the output root is the one that matters.
	ConfigPath string

	// Hints are the SDK calls found by the detect plugin, sorted by file then
	// byte offset.
	Hints []sdkdetect.Hint

	// UnitFiles maps execution unit ID to the sorted set of source paths that
	// belong to it. A file may appear under several units.
	UnitFiles map[string][]string

	// ClaimedFiles maps a source path to the static unit that claimed it, so
	// the execution-unit closure can leave those files out of compute bundles.
	ClaimedFiles map[string]string

	// Notice, when set, reports something worth telling the user that is not a
	// diagnostic about their code -- an optional tool that was missing, for
	// instance.
	Notice func(string)

	// Embedded maps a declaring source file to the paths cloudcc.embed_assets
	// claimed from it. Those files travel with whichever units bundle the
	// declaring file, even though no import reaches them.
	Embedded map[string][]string
}

// NewContext returns a context ready for the plugin chain. Files starts empty;
// the input plugin fills it, which is why it depends on the config plugin
// (out_dir must be known before the walk can exclude a previous output tree).
func NewContext(cfg *config.App, srcRoot string, out afero.Fs) *Context {
	return &Context{
		Config:       cfg,
		Files:        source.NewSet(srcRoot),
		Graph:        ir.NewProgram(),
		Diags:        &diag.Diagnostics{},
		Out:          out,
		SrcRoot:      srcRoot,
		UnitFiles:    map[string][]string{},
		ClaimedFiles: map[string]string{},
		Embedded:     map[string][]string{},
	}
}

// Failed reports whether an error-severity diagnostic has been recorded.
//
// Generating stages check this and return early. Diagnostics accumulate so the
// user sees every problem at once (D20), but there is no point resolving or
// rendering a program that is already known to be wrong -- and doing so only
// produces a second, confusing error about a decision the first one explains.
func (c *Context) Failed() bool { return c.Diags.HasErrors() }

// Pos builds a diagnostic position for a byte offset in a source file.
func (c *Context) Pos(path string, offset int) diag.Position {
	f, ok := c.Files.Get(path)
	if !ok {
		return diag.Position{File: path}
	}
	line, col := f.PositionAt(offset)
	return diag.Position{File: path, Line: line, Col: col}
}

// HintPos builds a diagnostic position for a hint.
func (c *Context) HintPos(h sdkdetect.Hint) diag.Position {
	return c.Pos(h.File, h.Span[0])
}

// HintsFor returns the detected hints for one capability, in stable order.
func (c *Context) HintsFor(capability string) []sdkdetect.Hint {
	var out []sdkdetect.Hint
	for _, h := range c.Hints {
		if h.Capability == capability {
			out = append(out, h)
		}
	}
	return out
}

// UnitsFor returns the sorted execution unit IDs whose file set contains path.
func (c *Context) UnitsFor(path string) []string {
	var out []string
	for _, id := range config.SortedKeys(c.UnitFiles) {
		for _, f := range c.UnitFiles[id] {
			if f == path {
				out = append(out, id)
				break
			}
		}
	}
	return out
}

// Schedule returns the plugins in dependency order. Unknown dependencies and
// cycles are reported here, at build time, rather than surfacing as a
// mysterious runtime ordering bug.
func Schedule(plugins []Plugin) ([]Plugin, error) {
	byName := map[string]Plugin{}
	g := graph.New[Plugin]()
	for _, p := range plugins {
		if _, dup := byName[p.Name()]; dup {
			return nil, fmt.Errorf("duplicate plugin name %q", p.Name())
		}
		byName[p.Name()] = p
		g.Add(p.Name(), p)
	}
	for _, p := range plugins {
		for _, dep := range p.Deps() {
			if !g.Has(dep) {
				return nil, fmt.Errorf("plugin %q depends on unknown plugin %q", p.Name(), dep)
			}
			g.Connect(dep, p.Name())
		}
	}
	order, err := g.TopoSort()
	if err != nil {
		return nil, fmt.Errorf("plugin schedule: %w", err)
	}
	out := make([]Plugin, 0, len(order))
	for _, name := range order {
		out = append(out, byName[name])
	}
	return out, nil
}

// Compiler runs a scheduled plugin chain.
type Compiler struct {
	ordered []Plugin
	// Trace, when non-nil, is called with each plugin name before it runs.
	Trace func(name string)
}

// NewCompiler schedules plugins and returns a runnable compiler.
func NewCompiler(plugins []Plugin) (*Compiler, error) {
	ordered, err := Schedule(plugins)
	if err != nil {
		return nil, err
	}
	return &Compiler{ordered: ordered}, nil
}

// Order returns the scheduled plugin names.
func (c *Compiler) Order() []string {
	out := make([]string, 0, len(c.ordered))
	for _, p := range c.ordered {
		out = append(out, p.Name())
	}
	return out
}

// Compile runs every plugin in order. A plugin returning an error aborts the
// run; recoverable problems belong in ctx.Diags instead.
func (c *Compiler) Compile(ctx *Context) error {
	for _, p := range c.ordered {
		if c.Trace != nil {
			c.Trace(p.Name())
		}
		if err := p.Transform(ctx); err != nil {
			return fmt.Errorf("%s: %w", p.Name(), err)
		}
	}
	return nil
}
