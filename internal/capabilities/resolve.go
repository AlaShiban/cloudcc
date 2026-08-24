package capabilities

import (
	"fmt"

	"github.com/cloudcompiler/cloudcc/internal/compiler"
	"github.com/cloudcompiler/cloudcc/internal/config"
	"github.com/cloudcompiler/cloudcc/internal/iac"
	_ "github.com/cloudcompiler/cloudcc/internal/iac/pulumi_ts" // registers the pulumi-ts backend
	"github.com/cloudcompiler/cloudcc/internal/ir"
	"github.com/cloudcompiler/cloudcc/internal/provider/aws"
	"github.com/cloudcompiler/cloudcc/internal/sanitize"
)

// ResolveAWSPlugin expands every intent into concrete AWS resources.
//
// This is the only plugin allowed to create resource nodes; capability plugins
// create intents and nothing else (D7). It runs after shims because the shim
// stage decides how each unit is packaged, which is what the compute mapping
// needs to know.
type ResolveAWSPlugin struct{ base }

// NewResolveAWSPlugin returns the resolve:aws stage.
func NewResolveAWSPlugin() *ResolveAWSPlugin {
	return &ResolveAWSPlugin{base{name: PluginResolveAWS, deps: []string{PluginShims, PluginValidate}}}
}

func (p *ResolveAWSPlugin) Transform(ctx *compiler.Context) error {
	if ctx.Failed() {
		return nil
	}
	if ctx.Config.Provider != config.ProviderAWS {
		return fmt.Errorf("provider %q has no resolver", ctx.Config.Provider)
	}
	r := &aws.Resolver{
		App:       ctx.Config.App,
		Program:   ctx.Graph,
		Config:    ctx.Config,
		StaticDir: StaticDir,
	}
	return r.Resolve()
}

// RenderPlugin hands the resolved program to the configured IaC backend.
type RenderPlugin struct {
	base
	backend string
}

// NewRenderPlugin returns the render stage for the given backend.
func NewRenderPlugin(backend string) *RenderPlugin {
	return &RenderPlugin{
		base:    base{name: PluginRender, deps: []string{PluginResolveAWS}},
		backend: backend,
	}
}

func (p *RenderPlugin) Transform(ctx *compiler.Context) error {
	if ctx.Failed() {
		return nil
	}
	backend, err := iac.Get(p.backend)
	if err != nil {
		return err
	}
	return backend.Emit(iac.Request{
		Program: ctx.Graph,
		Config:  ctx.Config,
		Out:     ctx.Out,
		UnitEnv: unitConfigEnv(ctx),
	})
}

// unitConfigEnv maps each execution unit to the config values it reads. A
// secret value is delivered from the encrypted stack config rather than being
// written into the generated source (D21).
func unitConfigEnv(ctx *compiler.Context) map[string]map[string]ir.EnvBinding {
	out := map[string]map[string]ir.EnvBinding{}
	for _, in := range ctx.Graph.IntentsOfKind(config.KindExecutionUnit) {
		unit := in.(*ir.ExecUnit)
		bindings := map[string]ir.EnvBinding{}
		for _, edge := range ctx.Graph.EdgesFrom(unit.Key(), ir.EdgeUses) {
			if edge.To.Kind != config.KindConfig {
				continue
			}
			v, ok := ctx.Graph.Intent(edge.To)
			if !ok {
				continue
			}
			cv := v.(*ir.ConfigVar)
			name := aws.EnvConfig(cv.ID)
			if cv.Secret {
				bindings[name] = ir.FromExpr(ir.Raw(sanitize.Identifier(cv.ID + "-secret")))
				continue
			}
			bindings[name] = ir.FromExpr(cv.Default)
		}
		// Always tell the shims where to find AWS, so a compiled unit can be
		// pointed at an emulator without touching its code (D15).
		bindings[aws.EnvEndpointOverride] = ir.FromExpr(
			ir.Raw(fmt.Sprintf("process.env.%s ?? \"\"", aws.EnvEndpointOverride)))
		out[unit.ID] = bindings
	}
	return out
}
