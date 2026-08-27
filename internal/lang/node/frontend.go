package node

import (
	"path"
	"sort"
	"strings"

	"github.com/cloudcompiler/cloudcc/internal/config"
	"github.com/cloudcompiler/cloudcc/internal/diag"
	"github.com/cloudcompiler/cloudcc/internal/ir"
	"github.com/cloudcompiler/cloudcc/internal/lang"
	"github.com/cloudcompiler/cloudcc/internal/sdkdetect"
	"github.com/cloudcompiler/cloudcc/internal/source"
)

// Frontend reads JavaScript and TypeScript.
type Frontend struct{}

func init() { lang.Register(Frontend{}) }

func (Frontend) Name() string { return lang.Node }

// Owns claims the JavaScript and TypeScript extensions. A .d.ts declaration
// file is deliberately excluded: it states types it does not implement, so
// bundling one would ship a module that shadows the real thing.
func (Frontend) Owns(p string) bool {
	if strings.HasSuffix(p, ".d.ts") || strings.HasSuffix(p, ".d.mts") {
		return false
	}
	for _, ext := range extensions {
		if strings.HasSuffix(p, ext) {
			return true
		}
	}
	return false
}

func (Frontend) Parse(f *source.File) error {
	_, grammar := grammarFor(f.Path)
	return f.Parse(lang.Node, grammar)
}

func (Frontend) Detect(f *source.File, diags *diag.Diagnostics) []sdkdetect.Hint {
	return detectHints(f, diags)
}

func (Frontend) Closure(files *source.Set, entry string, excluded map[string]string) ([]string, []lang.Unresolved) {
	return closure(files, entry, excluded)
}

func (Frontend) Routes(files *source.Set, unitFiles []string, appVar string) []ir.Route {
	return routes(files, unitFiles, appVar)
}

func (Frontend) RouterWarning(files *source.Set, unitFiles []string) (string, int, bool) {
	return routerWarning(files, unitFiles)
}

func (Frontend) MethodCalls(files *source.Set, unitFiles []string) []lang.MethodCall {
	return methodCalls(files, unitFiles)
}

func (Frontend) RemoteFunctions(files *source.Set, entry string) ([]lang.RemoteFunction, bool) {
	return remoteFunctions(files, entry), true
}

// EntrypointCandidates orders the modules that could serve as the entry for an
// undeclared single unit.
//
// A module that exposes an application wins outright: it is the one the runtime
// has to import. The fallback skips empty files, because a barrel index.js is
// often empty and often the shallowest module in the tree -- picking it yields
// a unit containing nothing, which is how the Python version of this was found
// to be wrong.
func (Frontend) EntrypointCandidates(files *source.Set, exposedIn []string, excluded map[string]string) []string {
	available := func(p string) bool {
		if _, claimed := excluded[p]; claimed {
			return false
		}
		f, ok := files.Get(p)
		return ok && f.Language() == lang.Node
	}

	var out []string
	for _, p := range exposedIn {
		if available(p) && !contains(out, p) {
			out = append(out, p)
		}
	}

	var rest []string
	for _, f := range files.FilesIn(lang.Node) {
		if available(f.Path) && !contains(out, f.Path) {
			rest = append(rest, f.Path)
		}
	}
	for _, preferred := range []string{"server.js", "server.ts", "index.js", "index.ts", "app.js", "app.ts", "main.js", "main.ts"} {
		for _, c := range rest {
			if c == preferred && !contains(out, c) {
				out = append(out, c)
			}
		}
	}
	sort.Slice(rest, func(i, j int) bool {
		di, dj := strings.Count(rest[i], "/"), strings.Count(rest[j], "/")
		if di != dj {
			return di < dj
		}
		return rest[i] < rest[j]
	})
	for _, pass := range []bool{true, false} {
		for _, c := range rest {
			if contains(out, c) {
				continue
			}
			f, ok := files.Get(c)
			hasBody := ok && len(strings.TrimSpace(string(f.Content))) > 0
			if hasBody == pass {
				out = append(out, c)
			}
		}
	}
	return out
}

// Rewrite splices hint calls in the output copy.
//
// PLAN-DEVIATION: the interface does not carry the unit's module system, so
// it is inferred here from the file's own extension. Deciding it once per unit
// -- from package.json#type as well -- is the N3 refinement; for a single file
// the extension is already decisive in the common cases (.mjs, .cjs) and ESM
// is the right default for the rest.
func (Frontend) Rewrite(f *source.File, hints []sdkdetect.Hint) error {
	return rewrite(f, hints, moduleSystemFor(f.Path) == "esm")
}

// moduleSystemFor reads the module system a file is written in.
func moduleSystemFor(p string) string {
	switch {
	case strings.HasSuffix(p, ".cjs"), strings.HasSuffix(p, ".cts"):
		return "cjs"
	default:
		return "esm"
	}
}

func (Frontend) RuntimeFiles() (map[string][]byte, error) { return runtimeFiles() }

func (Frontend) UnitFiles(u *ir.ExecUnit, opts lang.UnitOptions) (map[string][]byte, error) {
	return unitFiles(u, opts)
}

func (Frontend) Packaging(u *ir.ExecUnit) lang.Packaging {
	return lang.Packaging{
		LambdaRuntime: LambdaRuntime,
		Handler:       LambdaHandler,
		EntryModule:   entryModule(u.Entrypoint()),
		Artifact:      path.Join("build", u.ID+".zip"),
		ContainerPort: 8080,
	}
}

func (Frontend) PackagingScript(u *ir.ExecUnit) string {
	return packagingScript(u, u.Config().Type == config.TypeContainer)
}

func (Frontend) Tools() []lang.Tool {
	return []lang.Tool{
		{
			Name: "node", Binary: "node", Required: true,
			Install: "brew install node",
			Why:     "runs the generated infrastructure project and bundles Node execution units",
		},
		{
			Name: "esbuild", Binary: "esbuild", Required: false,
			Install: "npm install --global esbuild",
			Why:     "bundles Node execution units; npx is used when it is absent",
		},
	}
}

// entryModule is how the bundler refers to a unit's entrypoint.
func entryModule(entry string) string { return entry }

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

var _ lang.Frontend = Frontend{}
