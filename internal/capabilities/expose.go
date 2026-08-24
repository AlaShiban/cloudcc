package capabilities

import (
	"sort"
	"strings"

	"github.com/cloudcompiler/cloudcc/internal/compiler"
	"github.com/cloudcompiler/cloudcc/internal/config"
	"github.com/cloudcompiler/cloudcc/internal/ir"
	"github.com/cloudcompiler/cloudcc/internal/source"
	ts "github.com/tree-sitter/go-tree-sitter"
)

// httpVerbs are the FastAPI/Starlette route decorator methods recognised on an
// exposed application object.
var httpVerbs = map[string]string{
	"get":     "GET",
	"post":    "POST",
	"put":     "PUT",
	"delete":  "DELETE",
	"patch":   "PATCH",
	"head":    "HEAD",
	"options": "OPTIONS",
	"trace":   "TRACE",
}

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

		gw.Routes = p.routes(ctx, ctx.UnitFiles[unitID], appVar)
		if len(gw.Routes) == 0 {
			ctx.Diags.Warnf(ctx.HintPos(h), config.KindExpose,
				"no routes found on %q; the gateway will forward every path to the unit anyway", appVar)
		}
		p.warnAboutRouters(ctx, ctx.UnitFiles[unitID], h.File)

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

var decoratorQuery = source.MustQuery(`
  (decorated_definition
    (decorator
      (call
        function: (attribute object: (identifier) @obj attribute: (identifier) @verb)
        arguments: (argument_list) @args)) @decorator)
`)

// routes finds @<app>.<verb>("/path") decorators across a unit's files.
func (p *ExposePlugin) routes(ctx *compiler.Context, files []string, appVar string) []ir.Route {
	seen := map[string]bool{}
	var out []ir.Route
	for _, path := range files {
		f, ok := ctx.Files.Get(path)
		if !ok || !f.IsPython() {
			continue
		}
		f.Query(decoratorQuery, func(caps map[string]*ts.Node) {
			obj, verbNode, args := caps["obj"], caps["verb"], caps["args"]
			if obj == nil || verbNode == nil || args == nil || f.Text(obj) != appVar {
				return
			}
			verb, ok := httpVerbs[f.Text(verbNode)]
			if !ok {
				return
			}
			route, ok := firstStringArgument(f, args)
			if !ok {
				return
			}
			key := verb + " " + route
			if seen[key] {
				return
			}
			seen[key] = true
			out = append(out, ir.Route{Verb: verb, Path: route})
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Verb < out[j].Verb
	})
	return out
}

var routerQuery = source.MustQuery(`(call function: (identifier) @fn) @call`)

// warnAboutRouters reports APIRouter use, whose routes are not discovered.
// Routing still works at runtime -- the gateway forwards everything to the
// unit -- but the topology and the emitted route list will be incomplete.
func (p *ExposePlugin) warnAboutRouters(ctx *compiler.Context, files []string, hintFile string) {
	for _, path := range files {
		f, ok := ctx.Files.Get(path)
		if !ok || !f.IsPython() {
			continue
		}
		reported := false
		f.Query(routerQuery, func(caps map[string]*ts.Node) {
			if reported || f.Text(caps["fn"]) != "APIRouter" {
				return
			}
			reported = true
			ctx.Diags.Warnf(ctx.Pos(path, int(caps["call"].StartByte())), config.KindExpose,
				"routes registered on an APIRouter are not detected; "+
					"they will still be served, but will not appear in the topology")
		})
	}
}

// firstStringArgument returns the first positional string literal of a call.
func firstStringArgument(f *source.File, args *ts.Node) (string, bool) {
	for i := uint(0); i < args.NamedChildCount(); i++ {
		arg := args.NamedChild(i)
		if arg.Kind() == "keyword_argument" {
			continue
		}
		if arg.Kind() != "string" {
			return "", false
		}
		var sb strings.Builder
		for j := uint(0); j < arg.NamedChildCount(); j++ {
			c := arg.NamedChild(j)
			if c.Kind() == "interpolation" {
				return "", false
			}
			if c.Kind() == "string_content" || c.Kind() == "escape_sequence" {
				sb.WriteString(f.Text(c))
			}
		}
		return sb.String(), true
	}
	return "", false
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
