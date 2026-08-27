package python

import (
	"path"
	"sort"
	"strings"

	"github.com/cloudcompiler/cloudcc/internal/diag"
	"github.com/cloudcompiler/cloudcc/internal/ir"
	"github.com/cloudcompiler/cloudcc/internal/lang"
	runtimepy "github.com/cloudcompiler/cloudcc/internal/runtime/py"
	"github.com/cloudcompiler/cloudcc/internal/sdkdetect"
	"github.com/cloudcompiler/cloudcc/internal/source"
)

// Frontend reads Python.
type Frontend struct{}

func init() { lang.Register(Frontend{}) }

func (Frontend) Name() string { return lang.Python }

// Owns claims .py files. Other Python extensions -- .pyi stubs, .pyx -- are
// deliberately not source: a stub declares types it does not implement, and
// bundling one would ship a module that shadows the real thing.
func (Frontend) Owns(p string) bool { return strings.HasSuffix(p, ".py") }

func (Frontend) Parse(f *source.File) error {
	return f.Parse(lang.Python, source.PythonLanguage())
}

func (Frontend) Detect(f *source.File, diags *diag.Diagnostics) []sdkdetect.Hint {
	return detectHints(f, diags)
}

func (Frontend) Closure(files *source.Set, entry string, excluded map[string]string) ([]string, []lang.Unresolved) {
	paths, unresolved := closure(files, entry, excluded)
	out := make([]lang.Unresolved, 0, len(unresolved))
	for _, imp := range unresolved {
		out = append(out, lang.Unresolved{Rendered: renderImport(imp), Offset: imp.Offset})
	}
	return paths, out
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
// has to import, and a program that exposes something has said plainly where it
// starts. The fallback skips empty files, because a package's __init__.py is
// often empty and often the shallowest module in the tree -- picking it yields
// a unit containing nothing.
func (Frontend) EntrypointCandidates(files *source.Set, exposedIn []string, excluded map[string]string) []string {
	available := func(p string) bool {
		if _, claimed := excluded[p]; claimed {
			return false
		}
		f, ok := files.Get(p)
		return ok && f.Language() == lang.Python
	}

	var out []string
	for _, p := range exposedIn {
		if available(p) {
			out = append(out, p)
		}
	}

	var rest []string
	for _, f := range files.FilesIn(lang.Python) {
		if available(f.Path) && !contains(out, f.Path) {
			rest = append(rest, f.Path)
		}
	}
	for _, preferred := range []string{"app.py", "main.py"} {
		for _, c := range rest {
			if c == preferred {
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
	// Modules with something in them, then the empty ones as a last resort.
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

func (Frontend) Rewrite(f *source.File, hints []sdkdetect.Hint) error {
	return rewrite(f, hints)
}

func (Frontend) RuntimeFiles() (map[string][]byte, error) { return runtimepy.RuntimeFiles() }

// UnitFiles generates the entrypoint, the merged requirements and, for a
// container unit, the build file.
func (Frontend) UnitFiles(u *ir.ExecUnit, opts lang.UnitOptions) (map[string][]byte, error) {
	data := runtimepy.UnitTemplateData{
		Unit:        u.ID,
		EntryModule: source.ModuleName(u.Entrypoint()),
		ASGIApp:     u.ASGIApp,
	}
	out := map[string][]byte{}

	if opts.Container {
		if !opts.UserDockerfile {
			dockerfile, err := runtimepy.RenderDockerfile(data)
			if err != nil {
				return nil, err
			}
			out[runtimepy.DockerfileName] = dockerfile
		}
	} else {
		entry, err := runtimepy.RenderLambdaEntry(data)
		if err != nil {
			return nil, err
		}
		out[runtimepy.LambdaEntryModule+".py"] = entry
	}

	add := append([]string{}, runtimepy.ShimRequirements["base"]...)
	if u.ASGIApp != "" && !opts.Container {
		add = append(add, runtimepy.ShimRequirements["asgi"]...)
	}
	for _, library := range opts.Libraries {
		add = append(add, runtimepy.ShimRequirements[library]...)
	}
	out["requirements.txt"] = runtimepy.MergeRequirements(opts.Manifest, add)
	return out, nil
}

func (Frontend) Packaging(u *ir.ExecUnit) lang.Packaging {
	return lang.Packaging{
		LambdaRuntime: runtimepy.LambdaRuntime,
		Handler:       runtimepy.LambdaHandler,
		EntryModule:   source.ModuleName(u.Entrypoint()),
		Artifact:      path.Join("build", u.ID+".zip"),
		ContainerPort: 8080,
	}
}

func (Frontend) PackagingScript(u *ir.ExecUnit) string {
	// The wheels are resolved for the architecture the unit declared, because
	// the two cannot disagree: an architecture is part of a compiled
	// extension's filename, so a bundle built for the wrong one deploys and
	// then fails on its first invocation with a message about a missing module.
	return runtimepy.PackagingScript(u.ID, u.Config().Type == "ecs",
		runtimepy.PlatformFor(u.Config().Architecture()))
}

func (Frontend) Tools() []lang.Tool {
	return []lang.Tool{{
		Name:     "uv",
		Binary:   "uv",
		Required: true,
		Install:  "brew install uv",
		Why:      "installs Python dependencies when packaging execution units",
	}}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

var _ lang.Frontend = Frontend{}
