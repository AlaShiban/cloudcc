package python

import (
	"sort"

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

var decoratorQuery = source.MustQuery(source.PythonLanguage(), `
  (decorated_definition
    (decorator
      (call
        function: (attribute object: (identifier) @obj attribute: (identifier) @verb)
        arguments: (argument_list) @args)) @decorator)
`)

// routes finds @<app>.<verb>("/path") decorators across a unit's files.
func routes(files *source.Set, unitFiles []string, appVar string) []ir.Route {
	seen := map[string]bool{}
	var out []ir.Route
	for _, path := range unitFiles {
		f, ok := files.Get(path)
		if !ok || !f.Parsed() {
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

var routerQuery = source.MustQuery(source.PythonLanguage(), `(call function: (identifier) @fn) @call`)

// routerWarning reports the first APIRouter construction, whose routes are not
// discovered. Routing still works -- the gateway forwards everything to the
// unit -- but the topology and the route list will be incomplete, so the user
// is told rather than left to wonder.
func routerWarning(files *source.Set, unitFiles []string) (string, int, bool) {
	for _, path := range unitFiles {
		f, ok := files.Get(path)
		if !ok || !f.Parsed() {
			continue
		}
		var at int
		found := false
		f.Query(routerQuery, func(caps map[string]*ts.Node) {
			if found || f.Text(caps["fn"]) != "APIRouter" {
				return
			}
			found = true
			at = int(caps["call"].StartByte())
		})
		if found {
			return path, at, true
		}
	}
	return "", 0, false
}

// firstStringArgument returns the first positional string literal of a call,
// using the same decoder as hint arguments so a route path written as a
// concatenated or parenthesised string is read rather than silently skipped.
// One decoder, not two: the second one drifted and missed routes.
func firstStringArgument(f *source.File, args *ts.Node) (string, bool) {
	for i := uint(0); i < args.NamedChildCount(); i++ {
		arg := args.NamedChild(i)
		// A comment is a named child too, and a decorator can carry one.
		if arg.Kind() == "keyword_argument" || arg.Kind() == "comment" {
			continue
		}
		return stringLiteral(f, arg)
	}
	return "", false
}
