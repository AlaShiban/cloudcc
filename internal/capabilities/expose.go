package capabilities

import (
	"strings"

	"github.com/cloudcompiler/cloudcc/internal/compiler"
	"github.com/cloudcompiler/cloudcc/internal/config"
	"github.com/cloudcompiler/cloudcc/internal/ir"
)

// ExposePlugin turns cloudcc.expose hints into gateway intents and discovers the
// routes declared on the exposed application object.
type ExposePlugin struct{ base }

// NewExposePlugin returns the expose stage.
func NewExposePlugin() *ExposePlugin {
	return &ExposePlugin{base{name: PluginExpose, deps: []string{PluginExecUnits}}}
}

func (p *ExposePlugin) Transform(ctx *compiler.Context) error {
	for _, h := range ctx.HintsFor(config.KindExpose) {
		id := h.Str("id")
		if id == "" {
			id = "main"
		}
		appVar := h.Str("app")
		if !isIdentifier(appVar) {
			ctx.Diags.Errorf(ctx.HintPos(h), config.KindExpose,
				"the first argument must be the variable holding your ASGI app, not %q", appVar)
			continue
		}

		units := ctx.UnitsFor(h.File)
		if len(units) == 0 {
			ctx.Diags.Errorf(ctx.HintPos(h), config.KindExpose,
				"expose is declared in a file no execution unit imports")
			continue
		}
		if len(units) > 1 {
			ctx.Diags.Errorf(ctx.HintPos(h), config.KindExpose,
				"%s belongs to execution units %s; an exposed module must belong to exactly one",
				h.File, strings.Join(units, ", "))
			continue
		}
		unitID := units[0]
		front, ok := ctx.Frontend(unitID)
		if !ok {
			ctx.Diags.Errorf(ctx.HintPos(h), config.KindExpose,
				"execution unit %q has no language frontend", unitID)
			continue
		}

		gw := &ir.Expose{
			Target: orDefault(h.Str("target"), "public"),
			Unit:   unitID,
		}
		gw.ID = id
		if existing, seen := ctx.Graph.Intent(gw.Key()); seen {
			prev := existing.(*ir.Expose)
			ctx.Diags.Errorf(ctx.HintPos(h), config.KindExpose,
				"gateway %q is already declared for execution unit %q", id, prev.Unit)
			continue
		}

		gw.Routes = front.Routes(ctx.Files, ctx.UnitFiles[unitID], appVar)
		if len(gw.Routes) == 0 {
			ctx.Diags.Warnf(ctx.HintPos(h), config.KindExpose,
				"no routes found on %q; the gateway will forward every path to the unit anyway", appVar)
		}
		if file, offset, found := front.RouterWarning(ctx.Files, ctx.UnitFiles[unitID]); found {
			ctx.Diags.Warnf(ctx.Pos(file, offset), config.KindExpose,
				"routes registered on a router are not detected; "+
					"they will still be served, but will not appear in the topology")
		}

		cfg := ctx.Config.Lookup(config.KindExpose, id)
		if err := gw.Configure(cfg); err != nil {
			return err
		}
		ctx.Config.Record(config.KindExpose, id, cfg)
		ctx.Graph.AddIntent(gw)

		unitKey := ir.Key{Kind: config.KindExecutionUnit, ID: unitID}
		if unit, ok := ctx.Graph.Intent(unitKey); ok {
			unit.(*ir.ExecUnit).ASGIApp = appVar
			// The exposed module is the one the runtime must import, so it
			// becomes the unit's primary entrypoint.
			promoteEntrypoint(unit.(*ir.ExecUnit), h.File)
			ctx.Graph.Connect(gw.Key(), unitKey, ir.EdgeExposes)
		}
	}
	return nil
}

// promoteEntrypoint makes file the unit's primary entry module.
func promoteEntrypoint(unit *ir.ExecUnit, file string) {
	for i, e := range unit.Entrypoints {
		if e == file {
			unit.Entrypoints[0], unit.Entrypoints[i] = unit.Entrypoints[i], unit.Entrypoints[0]
			return
		}
	}
	unit.Entrypoints = append([]string{file}, unit.Entrypoints...)
}

func isIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_':
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
