package capabilities

import (
	"path"

	"github.com/cloudcompiler/cloudcc/internal/compiler"
	"github.com/cloudcompiler/cloudcc/internal/config"
	"github.com/cloudcompiler/cloudcc/internal/ir"
	"github.com/cloudcompiler/cloudcc/internal/lang"
	runtimepy "github.com/cloudcompiler/cloudcc/internal/runtime/py"
	"github.com/cloudcompiler/cloudcc/internal/sdkdetect"
)

// BinDir holds the generated helper scripts.
const BinDir = "bin"

// PackageScript is the path of the generated packaging script, relative to the
// output root.
const PackageScript = BinDir + "/package.sh"

// PushScript pushes container images. It is generated only when at least one
// unit is containerised, and must run after `pulumi up` has created the
// registries.
const PushScript = BinDir + "/push-images.sh"

// ShimsPlugin rewrites SDK hint calls in the output copy into real cloud
// clients, injects the _cloudcc_runtime package, generates the compute entrypoints,
// and writes the copy out (D13).
//
// Rewriting happens on a copy of the user's source; the input tree is never
// modified. Because the rewrite is a byte-range splice at each hint's recorded
// span, it must run after every capability plugin has read those hints.
type ShimsPlugin struct{ base }

// NewShimsPlugin returns the shims stage.
func NewShimsPlugin() *ShimsPlugin {
	return &ShimsPlugin{base{name: PluginShims, deps: []string{
		PluginExecUnits, PluginStaticUnits, PluginExpose,
		PluginPersist, PluginPubSub, PluginRemote, PluginConfigVars, PluginValidate,
	}}}
}

func (p *ShimsPlugin) Transform(ctx *compiler.Context) error {
	if ctx.Failed() {
		return nil
	}
	if err := p.rewriteSources(ctx); err != nil {
		return err
	}
	if err := writeUnitTrees(ctx); err != nil {
		return err
	}
	if err := p.injectRuntime(ctx); err != nil {
		return err
	}
	if err := p.writePackagingScript(ctx); err != nil {
		return err
	}
	return nil
}

// rewriteSources splices every hint call in the working copy. Each file is
// rewritten once even when several units bundle it, so shared modules stay
// byte-identical across units.
//
// Every Python file is visited, not only the ones containing hints: a module
// may import the SDK for a type annotation, or keep an import that is no
// longer used, and that import has to go either way. The SDK is not installed
// in a deployment bundle, so leaving one behind means the unit dies on its
// first import.
func (p *ShimsPlugin) rewriteSources(ctx *compiler.Context) error {
	byFile := map[string][]sdkdetect.Hint{}
	for _, h := range ctx.Hints {
		byFile[h.File] = append(byFile[h.File], h)
	}
	for _, f := range ctx.Files.ParsedFiles() {
		front, ok := lang.For(f.Path)
		if !ok {
			continue
		}
		if err := front.Rewrite(f, byFile[f.Path]); err != nil {
			return err
		}
	}
	return nil
}

// injectRuntime writes the _cloudcc_runtime package, the compute entrypoint and the
// merged requirements into each unit's output directory.
func (p *ShimsPlugin) injectRuntime(ctx *compiler.Context) error {
	// The runtime package is per-language and identical for every unit in that
	// language, so it is rendered once rather than once per unit.
	runtimeFiles := map[string]map[string][]byte{}

	for _, unitID := range config.SortedKeys(ctx.UnitFiles) {
		unit, ok := lookupUnit(ctx, unitID)
		if !ok {
			continue
		}
		front, ok := ctx.Frontend(unitID)
		if !ok {
			continue
		}

		if _, done := runtimeFiles[front.Name()]; !done {
			files, err := front.RuntimeFiles()
			if err != nil {
				return err
			}
			runtimeFiles[front.Name()] = files
		}
		for _, rel := range config.SortedKeys(runtimeFiles[front.Name()]) {
			if err := writeOut(ctx.Out, path.Join(unitID, rel), runtimeFiles[front.Name()][rel]); err != nil {
				return err
			}
		}

		pkg := front.Packaging(unit)
		unit.EntryModule = pkg.EntryModule
		unit.Runtime = pkg.LambdaRuntime
		unit.Artifact = pkg.Artifact

		container := unit.Config().Type == config.TypeContainer
		if container {
			unit.DockerfileProvided = userDockerfile(ctx, unitID)
		} else {
			unit.Handler = pkg.Handler
		}

		generated, err := front.UnitFiles(unit, lang.UnitOptions{
			Manifest:       p.manifest(ctx, front),
			Capabilities:   p.capabilities(ctx, unit),
			Libraries:      p.libraries(ctx, unit),
			Container:      container,
			UserDockerfile: unit.DockerfileProvided,
		})
		if err != nil {
			return err
		}
		for _, rel := range config.SortedKeys(generated) {
			if err := writeOut(ctx.Out, path.Join(unitID, rel), generated[rel]); err != nil {
				return err
			}
		}
	}
	return nil
}

// manifest returns the user's dependency manifest for a language, empty when
// they have none.
func (p *ShimsPlugin) manifest(ctx *compiler.Context, front lang.Frontend) []byte {
	name := ctx.Files.ManifestPath(front.Name())
	if name == "" {
		return nil
	}
	if f, ok := ctx.Files.Get(name); ok {
		return f.Content
	}
	return nil
}

// capabilities returns the capability kinds a unit uses, sorted. It is what
// decides which client libraries the bundle carries: a program with no cache
// does not ship a Redis client.
func (p *ShimsPlugin) capabilities(ctx *compiler.Context, unit *ir.ExecUnit) []string {
	seen := map[string]bool{}
	for _, e := range ctx.Graph.EdgesFrom(unit.Key(), ir.EdgeUses) {
		seen[e.To.Kind] = true
	}
	return config.SortedKeys(seen)
}

// libraries returns the client libraries a unit's stores declared, sorted. It
// is what decides the exact package a bundle carries: persist_redis alone does
// not say whether the program reached for ioredis or node-redis.
func (p *ShimsPlugin) libraries(ctx *compiler.Context, unit *ir.ExecUnit) []string {
	seen := map[string]bool{}
	for _, e := range ctx.Graph.EdgesFrom(unit.Key(), ir.EdgeUses) {
		intent, ok := ctx.Graph.Intent(e.To)
		if !ok {
			continue
		}
		if store, isStore := intent.(*ir.Persist); isStore && store.Library != "" {
			seen[store.Library] = true
		}
	}
	return config.SortedKeys(seen)
}

// writePackagingScript emits bin/package.sh. Pulumi does not install Python
// dependencies, so something has to; `cloudcc deploy` runs this before `up`.
func (p *ShimsPlugin) writePackagingScript(ctx *compiler.Context) error {
	var units []runtimepy.PackageUnit
	containers := false
	for _, id := range config.SortedKeys(ctx.UnitFiles) {
		unit, ok := lookupUnit(ctx, id)
		if !ok {
			continue
		}
		front, ok := ctx.Frontend(id)
		if !ok {
			continue
		}
		isContainer := unit.Config().Type == config.TypeContainer
		containers = containers || isContainer
		units = append(units, runtimepy.PackageUnit{
			ID:        id,
			Container: isContainer,
			// Each unit contributes the shell that builds it, so the script
			// itself does not have to know what any unit was written in.
			Fragment: front.PackagingScript(unit),
		})
	}

	script, err := runtimepy.RenderPackageScript(units)
	if err != nil {
		return err
	}
	if err := writeOut(ctx.Out, PackageScript, script); err != nil {
		return err
	}
	if err := ctx.Out.Chmod(PackageScript, 0o755); err != nil {
		return err
	}

	if !containers {
		return nil
	}
	push, err := runtimepy.RenderPushScript(units)
	if err != nil {
		return err
	}
	if err := writeOut(ctx.Out, PushScript, push); err != nil {
		return err
	}
	return ctx.Out.Chmod(PushScript, 0o755)
}

// userDockerfile reports whether the unit's bundle already contains a
// Dockerfile the user wrote.
func userDockerfile(ctx *compiler.Context, unitID string) bool {
	for _, rel := range ctx.UnitFiles[unitID] {
		if path.Base(rel) == runtimepy.DockerfileName {
			return true
		}
	}
	return false
}

func lookupUnit(ctx *compiler.Context, id string) (*ir.ExecUnit, bool) {
	in, ok := ctx.Graph.Intent(ir.Key{Kind: config.KindExecutionUnit, ID: id})
	if !ok {
		return nil, false
	}
	unit, ok := in.(*ir.ExecUnit)
	return unit, ok
}
