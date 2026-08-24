package capabilities

import (
	"path"

	"github.com/cloudcompiler/cc/internal/compiler"
	"github.com/cloudcompiler/cc/internal/config"
	"github.com/cloudcompiler/cc/internal/ir"
	runtimepy "github.com/cloudcompiler/cc/internal/runtime/py"
	"github.com/cloudcompiler/cc/internal/sdkdetect"
	"github.com/cloudcompiler/cc/internal/source"
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
// clients, injects the _cc_runtime package, generates the compute entrypoints,
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
		PluginPersist, PluginPubSub, PluginConfigVars, PluginValidate,
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
func (p *ShimsPlugin) rewriteSources(ctx *compiler.Context) error {
	byFile := map[string][]sdkdetect.Hint{}
	for _, h := range ctx.Hints {
		byFile[h.File] = append(byFile[h.File], h)
	}
	for _, rel := range config.SortedKeys(byFile) {
		f, ok := ctx.Files.Get(rel)
		if !ok {
			continue
		}
		if err := runtimepy.Rewrite(f, byFile[rel]); err != nil {
			return err
		}
	}
	return nil
}

// injectRuntime writes the _cc_runtime package, the compute entrypoint and the
// merged requirements into each unit's output directory.
func (p *ShimsPlugin) injectRuntime(ctx *compiler.Context) error {
	runtimeFiles, err := runtimepy.RuntimeFiles()
	if err != nil {
		return err
	}

	for _, unitID := range config.SortedKeys(ctx.UnitFiles) {
		unit, ok := lookupUnit(ctx, unitID)
		if !ok {
			continue
		}
		computeType := unit.Config().Type
		entryModule := source.ModuleName(unit.Entrypoint())

		unit.EntryModule = entryModule

		for _, rel := range config.SortedKeys(runtimeFiles) {
			if err := writeOut(ctx.Out, path.Join(unitID, rel), runtimeFiles[rel]); err != nil {
				return err
			}
		}

		data := runtimepy.UnitTemplateData{
			Unit:        unitID,
			EntryModule: entryModule,
			ASGIApp:     unit.ASGIApp,
		}

		switch computeType {
		case "lambda":
			entry, err := runtimepy.RenderLambdaEntry(data)
			if err != nil {
				return err
			}
			if err := writeOut(ctx.Out, path.Join(unitID, runtimepy.LambdaEntryModule+".py"), entry); err != nil {
				return err
			}
			unit.Handler = runtimepy.LambdaHandler
		case "ecs":
			if userDockerfile(ctx, unitID) {
				// The user's Dockerfile wins; it was already copied through
				// with the rest of the unit's files (D13).
				unit.DockerfileProvided = true
				break
			}
			dockerfile, err := runtimepy.RenderDockerfile(data)
			if err != nil {
				return err
			}
			if err := writeOut(ctx.Out, path.Join(unitID, runtimepy.DockerfileName), dockerfile); err != nil {
				return err
			}
		}

		reqs, err := p.requirements(ctx, unitID, unit)
		if err != nil {
			return err
		}
		if err := writeOut(ctx.Out, path.Join(unitID, "requirements.txt"), reqs); err != nil {
			return err
		}
	}
	return nil
}

// requirements merges the shim dependencies into the unit's requirements.txt,
// adding only what the unit's declared capabilities actually need.
func (p *ShimsPlugin) requirements(ctx *compiler.Context, unitID string, unit *ir.ExecUnit) ([]byte, error) {
	var existing []byte
	if manifest := ctx.Files.RequirementsPath; manifest != "" {
		if f, ok := ctx.Files.Get(manifest); ok {
			existing = f.Content
		}
	}

	add := append([]string{}, runtimepy.ShimRequirements["base"]...)
	if unit.ASGIApp != "" && unit.Config().Type == "lambda" {
		add = append(add, runtimepy.ShimRequirements["asgi"]...)
	}
	for _, e := range ctx.Graph.EdgesFrom(unit.Key(), ir.EdgeUses) {
		if extra, ok := runtimepy.ShimRequirements[e.To.Kind]; ok {
			add = append(add, extra...)
		}
	}
	return runtimepy.MergeRequirements(existing, add), nil
}

// writePackagingScript emits bin/package.sh. Pulumi does not install Python
// dependencies, so something has to; `cc deploy` runs this before `up`.
func (p *ShimsPlugin) writePackagingScript(ctx *compiler.Context) error {
	var units []runtimepy.PackageUnit
	containers := false
	for _, id := range config.SortedKeys(ctx.UnitFiles) {
		unit, ok := lookupUnit(ctx, id)
		if !ok {
			continue
		}
		isContainer := unit.Config().Type == "ecs"
		containers = containers || isContainer
		units = append(units, runtimepy.PackageUnit{ID: id, Container: isContainer})
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
