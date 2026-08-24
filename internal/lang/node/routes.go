package node

import (
	"sort"
	"strings"

	"github.com/cloudcompiler/cloudcc/internal/ir"
	"github.com/cloudcompiler/cloudcc/internal/lang"
	"github.com/cloudcompiler/cloudcc/internal/source"
	ts "github.com/tree-sitter/go-tree-sitter"
)

// httpVerbs are the methods recognised on an exposed application.
//
// `app.get(path, handler)` is how Express, Fastify and Hono all declare a
// route, so one pattern covers three frameworks. That is a happy accident of
// convergent design rather than anything clever here.
var httpVerbs = map[string]string{
	"get":     "GET",
	"post":    "POST",
	"put":     "PUT",
	"delete":  "DELETE",
	"patch":   "PATCH",
	"head":    "HEAD",
	"options": "OPTIONS",
	"all":     "ALL",
}

// routes finds `app.<verb>("/path", ...)` calls on the exposed application.
func routes(files *source.Set, unitFiles []string, appVar string) []ir.Route {
	seen := map[string]bool{}
	var out []ir.Route

	for _, p := range unitFiles {
		f, ok := files.Get(p)
		if !ok || !f.Parsed() {
			continue
		}
		f.Query(queriesFor(f).member, func(caps map[string]*ts.Node) {
			obj, method, call := caps["obj"], caps["method"], caps["call"]
			if obj == nil || method == nil || call == nil || f.Text(obj) != appVar {
				return
			}
			verb, ok := httpVerbs[f.Text(method)]
			if !ok {
				return
			}
			args := call.ChildByFieldName("arguments")
			if args == nil {
				return
			}
			route, ok := firstStringArgument(f, args)
			if !ok {
				return
			}
			// `app.use(...)` and `app.all(...)` mount rather than declare, so
			// a mount path is not a route in its own right.
			if verb == "ALL" {
				return
			}
			key := verb + " " + route
			if seen[key] {
				return
			}
			seen[key] = true
			out = append(out, ir.Route{Verb: verb, Path: normalisePath(route)})
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

// normalisePath rewrites Express's `:param` into the `{param}` form the rest of
// the compiler and the topology already use, so a route reads the same however
// it was written.
func normalisePath(p string) string {
	if !strings.Contains(p, ":") {
		return p
	}
	segments := strings.Split(p, "/")
	for i, seg := range segments {
		if strings.HasPrefix(seg, ":") && len(seg) > 1 {
			name := strings.TrimSuffix(strings.TrimPrefix(seg, ":"), "?")
			segments[i] = "{" + name + "}"
		}
	}
	return strings.Join(segments, "/")
}

// firstStringArgument returns the first positional string literal of a call,
// using the same decoder as hint arguments so a path written as a template
// literal or a concatenation is read rather than silently skipped.
func firstStringArgument(f *source.File, args *ts.Node) (string, bool) {
	for i := uint(0); i < args.NamedChildCount(); i++ {
		arg := args.NamedChild(i)
		if arg.Kind() == "comment" {
			continue
		}
		return stringLiteral(f, arg)
	}
	return "", false
}

// routerWarning reports the first Router construction, whose routes are not
// discovered. Routing still works -- the gateway forwards everything to the
// unit -- but the topology and the route list will be incomplete, so the user
// is told rather than left to wonder.
func routerWarning(files *source.Set, unitFiles []string) (string, int, bool) {
	for _, p := range unitFiles {
		f, ok := files.Get(p)
		if !ok || !f.Parsed() {
			continue
		}
		var at int
		found := false
		f.Query(queriesFor(f).member, func(caps map[string]*ts.Node) {
			if found {
				return
			}
			// express.Router() is the shape; a bare `Router()` is caught by
			// the identifier pass below.
			if f.Text(caps["method"]) == "Router" {
				found = true
				at = int(caps["call"].StartByte())
			}
		})
		if found {
			return p, at, true
		}
	}
	return "", 0, false
}

// methodCalls returns every `receiver.method(...)` site in a unit, which is how
// a publisher is told apart from a subscriber.
func methodCalls(files *source.Set, unitFiles []string) []lang.MethodCall {
	var out []lang.MethodCall
	for _, p := range unitFiles {
		f, ok := files.Get(p)
		if !ok || !f.Parsed() {
			continue
		}
		f.Query(queriesFor(f).member, func(caps map[string]*ts.Node) {
			obj, method, call := caps["obj"], caps["method"], caps["call"]
			if obj == nil || method == nil || call == nil {
				return
			}
			out = append(out, lang.MethodCall{
				Object: f.Text(obj),
				Method: f.Text(method),
				File:   p,
				Offset: int(call.StartByte()),
			})
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Offset < out[j].Offset
	})
	return out
}
