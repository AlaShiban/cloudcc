package capabilities

import (
	"sort"
	"strings"

	"github.com/cloudcompiler/cloudcc/internal/compiler"
	"github.com/cloudcompiler/cloudcc/internal/config"
	"github.com/cloudcompiler/cloudcc/internal/diag"
	"github.com/cloudcompiler/cloudcc/internal/ir"
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

		var files []string
		for _, entry := range entrypoints[id] {
			closure, unresolved := Closure(ctx.Files, entry, ctx.ClaimedFiles)
			files = union(files, closure)
			for _, imp := range unresolved {
				ctx.Diags.Warnf(ctx.Pos(entry, imp.Offset), config.KindExecutionUnit,
					"relative import %q could not be resolved to a file in the source tree; "+
						"it will not be bundled into execution unit %q", renderImport(imp), id)
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

// defaultEntrypoint picks the entry module for the implicit single unit.
//
// The module that exposes an ASGI app wins outright: it is the one the runtime
// has to import, and a program that exposes something has told us plainly
// where it starts. Only when nothing is exposed does this fall back to
// convention and then to layout.
//
// The fallback deliberately skips empty files. A package's __init__.py is
// often empty and often the shallowest Python file in the tree, so a naive
// "shallowest wins" rule picks it and produces a unit containing nothing --
// which is how this was found.
func (p *ExecUnitsPlugin) defaultEntrypoint(ctx *compiler.Context) string {
	available := func(path string) bool {
		if _, claimed := ctx.ClaimedFiles[path]; claimed {
			return false
		}
		f, ok := ctx.Files.Get(path)
		return ok && f.IsPython()
	}

	// 1. A module that exposes an application.
	for _, h := range ctx.HintsFor(config.KindExpose) {
		if available(h.File) {
			return h.File
		}
	}

	var candidates []string
	for _, f := range ctx.Files.PythonFiles() {
		if available(f.Path) {
			candidates = append(candidates, f.Path)
		}
	}
	if len(candidates) == 0 {
		return ""
	}

	// 2. A conventionally named module at the root.
	for _, preferred := range []string{"app.py", "main.py"} {
		for _, c := range candidates {
			if c == preferred {
				return c
			}
		}
	}

	// 3. The shallowest module with something in it, alphabetically.
	sort.Slice(candidates, func(i, j int) bool {
		di, dj := strings.Count(candidates[i], "/"), strings.Count(candidates[j], "/")
		if di != dj {
			return di < dj
		}
		return candidates[i] < candidates[j]
	})
	for _, c := range candidates {
		if f, ok := ctx.Files.Get(c); ok && len(strings.TrimSpace(string(f.Content))) > 0 {
			return c
		}
	}
	return candidates[0]
}

// sharedAssets returns every non-Python file that no capability has claimed.
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
		if f.IsPython() || embedded[f.Path] {
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
	for _, f := range ctx.Files.PythonFiles() {
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

func renderImport(imp pyImport) string {
	var b strings.Builder
	b.WriteString("from ")
	b.WriteString(strings.Repeat(".", imp.Level))
	b.WriteString(imp.Module)
	if len(imp.Names) > 0 {
		b.WriteString(" import ")
		b.WriteString(strings.Join(imp.Names, ", "))
	}
	return b.String()
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
