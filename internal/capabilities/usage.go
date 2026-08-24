package capabilities

import (
	"sort"

	"github.com/cloudcompiler/cloudcc/internal/compiler"
	"github.com/cloudcompiler/cloudcc/internal/source"
	ts "github.com/tree-sitter/go-tree-sitter"
)

var methodCallQuery = source.MustQuery(
	`(call function: (attribute object: (identifier) @obj attribute: (identifier) @method)) @call`)

// methodCall is one `name.method(...)` call site.
type methodCall struct {
	Object string
	Method string
	File   string
	Offset int
}

// findMethodCalls returns every `<object>.<method>(...)` call in the given
// files. Identifier-name matching is deliberate: `from shared.store import
// events` keeps the binding's name, so a name-based scan follows a handle
// across modules without needing a full symbol table.
func findMethodCalls(ctx *compiler.Context, files []string) []methodCall {
	var out []methodCall
	for _, path := range files {
		f, ok := ctx.Files.Get(path)
		if !ok || !f.IsPython() {
			continue
		}
		f.Query(methodCallQuery, func(caps map[string]*ts.Node) {
			obj, method, call := caps["obj"], caps["method"], caps["call"]
			if obj == nil || method == nil || call == nil {
				return
			}
			out = append(out, methodCall{
				Object: f.Text(obj),
				Method: f.Text(method),
				File:   path,
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

var identifierQuery = source.MustQuery(`(identifier) @id`)

// referencesIdentifier reports whether any of the files mention name.
func referencesIdentifier(ctx *compiler.Context, files []string, name string) bool {
	if name == "" {
		return false
	}
	for _, path := range files {
		f, ok := ctx.Files.Get(path)
		if !ok || !f.IsPython() {
			continue
		}
		found := false
		f.Query(identifierQuery, func(caps map[string]*ts.Node) {
			if !found && f.Text(caps["id"]) == name {
				found = true
			}
		})
		if found {
			return true
		}
	}
	return false
}
