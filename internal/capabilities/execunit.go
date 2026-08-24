package capabilities

import (
	"sort"

	"github.com/cloudcompiler/cloudcc/internal/compiler"
	"github.com/cloudcompiler/cloudcc/internal/config"
	"github.com/cloudcompiler/cloudcc/internal/diag"
	"github.com/cloudcompiler/cloudcc/internal/ir"
	"github.com/cloudcompiler/cloudcc/internal/lang"
)

// DefaultUnitID is the single unit every program gets when it declares none.
const DefaultUnitID = "main"

// ExecUnitsPlugin splits the program into execution units.
//
// Each cloudcc.execution_unit(id=...) call marks its module as an entrypoint. A
// unit's files are the transitive local-import closure of its entrypoints; a
// file reached from two entrypoints belongs to both units and is copied into
// each bundle. With no hints at all the whole program is one unit, "main".
//
// Klotho 1 shipped this machinery but never exercised it. Multi-unit is a
// first-class, tested case here (D4).
type ExecUnitsPlugin struct{ base }

// NewExecUnitsPlugin returns the exec-units stage. It depends on static-units
// so that claimed assets are already out of the pool.
func NewExecUnitsPlugin() *ExecUnitsPlugin {
	return &ExecUnitsPlugin{base{name: PluginExecUnits, deps: []string{
		PluginDetect, PluginStaticUnits, PluginEmbedAssets,
	}}}
}

func (p *ExecUnitsPlugin) Transform(ctx *compiler.Context) error {
	entrypoints := map[string][]string{} // unit id -> sorted entry modules
	for _, h := range ctx.HintsFor(config.KindExecutionUnit) {
		id := h.ID()
		if h.Enclosing != "" {
			ctx.Diags.Errorf(ctx.HintPos(h), config.KindExecutionUnit,
				"execution_unit must be called at module level, not inside %s()", h.Enclosing)
			continue
		}
		if owner, dup := findEntry(entrypoints, h.File); dup && owner != id {
			ctx.Diags.Errorf(ctx.HintPos(h), config.KindExecutionUnit,
				"%s is already the entrypoint of execution unit %q", h.File, owner)
			continue
		}
		entrypoints[id] = insertUnique(entrypoints[id], h.File)

		// A type given at the call site is the weakest layer; cloudcc.yaml wins.
		if typ := h.Str("type"); typ != "" {
			existing := ctx.Config.ExecutionUnits[id]
			if existing.Type == "" {
				ctx.Config.Record(config.KindExecutionUnit, id, config.ResourceConfig{Type: typ})
			}
		}
	}

	if len(entrypoints) == 0 {
		entry := p.defaultEntrypoint(ctx)
		if entry == "" {
			// No Python at all: still emit a unit so static-only programs and
			// diagnostics have somewhere to hang.
			ctx.Diags.Warnf(diag.Position{}, config.KindExecutionUnit,
				"no Python source found; execution unit %q will be empty", DefaultUnitID)
		}
		entrypoints[DefaultUnitID] = compact([]string{entry})
	}

	assigned := map[string]bool{}
	for _, id := range config.SortedKeys(entrypoints) {
		unit := &ir.ExecUnit{Entrypoints: entrypoints[id]}
		unit.ID = id

		// The unit's language comes from its entrypoint, so a program can
		// hold units written in different languages.
		front, ok := lang.For(entrypoints[id][0])
		if !ok {
			ctx.Diags.Errorf(diag.Position{File: entrypoints[id][0]}, config.KindExecutionUnit,
				"no language frontend claims this file")
			continue
		}
		ctx.Language[id] = front.Name()
		unit.Language = front.Name()

		var files []string
		for _, entry := range entrypoints[id] {
			reached, unresolved := front.Closure(ctx.Files, entry, ctx.ClaimedFiles)
			files = union(files, reached)
			for _, imp := range unresolved {
				ctx.Diags.Warnf(ctx.Pos(entry, imp.Offset), config.KindExecutionUnit,
					"the import %q could not be resolved to a file in the source tree; "+
						"it will not be bundled into execution unit %q", imp.Rendered, id)
			}
		}
		// Everything that is not Python source -- templates, data files,
		// manifests -- travels with every unit, because deciding which unit
		// reads a data file is not something static analysis can do.
		files = union(files, p.sharedAssets(ctx))
		// Files claimed by cloudcc.embed_assets travel with the units that bundle
		// the module which claimed them.
		for _, declaring := range config.SortedKeys(ctx.Embedded) {
			if containsPath(files, declaring) {
				files = union(files, ctx.Embedded[declaring])
			}
		}

		for _, f := range files {
			assigned[f] = true
		}
		unit.Files = files

		cfg := ctx.Config.Lookup(config.KindExecutionUnit, id)
		if err := unit.Configure(cfg); err != nil {
			return err
		}
		ctx.Config.Record(config.KindExecutionUnit, id, cfg)
		ctx.UnitFiles[id] = files
		ctx.Graph.AddIntent(unit)
	}

	p.warnUnreachable(ctx, assigned)
	return nil
}

// defaultEntrypoint picks the entry module for the implicit single unit by
// asking each frontend for its candidates, best first. Which module a language
// considers a sensible entry is a property of that language.
func (p *ExecUnitsPlugin) defaultEntrypoint(ctx *compiler.Context) string {
	var exposedIn []string
	for _, h := range ctx.HintsFor(config.KindExpose) {
		exposedIn = append(exposedIn, h.File)
	}
	for _, front := range lang.All() {
		if candidates := front.EntrypointCandidates(ctx.Files, exposedIn, ctx.ClaimedFiles); len(candidates) > 0 {
			return candidates[0]
		}
	}
	return ""
}

// sharedAssets returns every non-source file that no capability has claimed.
//
// A file claimed by cloudcc.embed_assets is deliberately excluded: claiming it is
// how a program says which unit owns it, so shipping it to every unit as well
// would make the hint pointless.
func (p *ExecUnitsPlugin) sharedAssets(ctx *compiler.Context) []string {
	embedded := map[string]bool{}
	for _, paths := range ctx.Embedded {
		for _, path := range paths {
			embedded[path] = true
		}
	}
	var out []string
	for _, f := range ctx.Files.Files() {
		if f.Parsed() || embedded[f.Path] {
			continue
		}
		if _, claimed := ctx.ClaimedFiles[f.Path]; claimed {
			continue
		}
		out = append(out, f.Path)
	}
	return out
}

// warnUnreachable reports Python files no unit reached. They are pruned from
// the output, so silence here would mean silently dropping code.
func (p *ExecUnitsPlugin) warnUnreachable(ctx *compiler.Context, assigned map[string]bool) {
	for _, f := range ctx.Files.ParsedFiles() {
		if assigned[f.Path] {
			continue
		}
		if _, claimed := ctx.ClaimedFiles[f.Path]; claimed {
			continue
		}
		ctx.Diags.Warnf(diag.Position{File: f.Path}, config.KindExecutionUnit,
			"no execution unit imports this file; it will not be deployed")
	}
}

func findEntry(entrypoints map[string][]string, file string) (string, bool) {
	for _, id := range config.SortedKeys(entrypoints) {
		for _, f := range entrypoints[id] {
			if f == file {
				return id, true
			}
		}
	}
	return "", false
}

func insertUnique(s []string, v string) []string {
	for _, existing := range s {
		if existing == v {
			return s
		}
	}
	s = append(s, v)
	sort.Strings(s)
	return s
}

func containsPath(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func compact(s []string) []string {
	var out []string
	for _, v := range s {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}
