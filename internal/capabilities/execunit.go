package capabilities

import (
	"sort"

	"github.com/cloudcompiler/cloudcc/internal/compiler"
	"github.com/cloudcompiler/cloudcc/internal/config"
	"github.com/cloudcompiler/cloudcc/internal/diag"
	"github.com/cloudcompiler/cloudcc/internal/ir"
	"github.com/cloudcompiler/cloudcc/internal/lang"
	"github.com/cloudcompiler/cloudcc/internal/sdkdetect"
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

		excluded := p.closureBarriers(ctx, id, entrypoints)

		var files []string
		for _, entry := range entrypoints[id] {
			reached, unresolved := front.Closure(ctx.Files, entry, excluded)
			files = union(files, reached)
			for _, imp := range unresolved {
				why := ""
				if imp.Why != "" {
					why = " -- " + imp.Why
				}
				ctx.Diags.Warnf(ctx.Pos(entry, imp.Offset), config.KindExecutionUnit,
					"the import %q could not be resolved to a file in the source tree; "+
						"it will not be bundled into execution unit %q%s", imp.Rendered, id, why)
			}
		}
		// Everything that is not Python source -- templates, data files,
		// manifests -- travels with every unit, because deciding which unit
		// reads a data file is not something static analysis can do.
		files = union(files, p.sharedAssets(ctx, unit.Language))
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

// closureBarriers returns the files unit id's import walk must stop at: those
// claimed by a static unit, plus the entry module of every unit some part of
// the program reaches through cloudcc.remote.
//
// The second half is what makes a remote call a boundary rather than a comment.
// A caller imports the callee's module so that the uncompiled program runs in
// one process and so that an editor can check the call; without this cut that
// import would also drag the callee's module into the caller's bundle, and
// then:
//
//   - the caller would be granted IAM on the callee's stores and handed their
//     environment, because both are derived from the files a unit bundles --
//     so the isolation the boundary exists to create would not exist;
//   - the caller's bundle would carry the callee's dependencies, which is the
//     cost a service split is supposed to avoid.
//
// A unit named by a remote() call anywhere is a barrier for every unit except
// itself. That is a property of the program rather than of any one caller,
// which is what keeps this from needing to know a unit's files before it has
// computed them.
func (p *ExecUnitsPlugin) closureBarriers(ctx *compiler.Context, id string, entrypoints map[string][]string) map[string]string {
	targets := map[string]bool{}
	for _, h := range ctx.HintsFor(sdkdetect.CapRemote) {
		if target := h.ID(); target != "" && target != id {
			targets[target] = true
		}
	}
	if len(targets) == 0 {
		return ctx.ClaimedFiles
	}

	// Copied rather than written through: ClaimedFiles is shared, and the
	// barrier set is per unit.
	out := make(map[string]string, len(ctx.ClaimedFiles)+len(targets))
	for path, claimer := range ctx.ClaimedFiles {
		out[path] = claimer
	}
	for target := range targets {
		for _, entry := range entrypoints[target] {
			out[entry] = target
		}
	}
	return out
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
// sharedAssets returns the files that travel with a unit of the given
// language: everything that is not source, minus the dependency manifests
// belonging to *other* languages.
//
// That exclusion matters once one application holds units in more than one
// language. A Python bundle has no use for package.json, and shipping it is at
// best dead weight and at worst a puzzle for whoever opens the bundle. A
// unit's own manifest is read by the compiler and regenerated per unit, so it
// does not need to travel either -- but it is left in, because a program may
// legitimately read its own package.json at runtime.
func (p *ExecUnitsPlugin) sharedAssets(ctx *compiler.Context, language string) []string {
	foreign := map[string]bool{}
	for _, other := range lang.Names() {
		if other == language {
			continue
		}
		if manifest := ctx.Files.ManifestPath(other); manifest != "" {
			foreign[manifest] = true
		}
	}

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
		if foreign[f.Path] {
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
