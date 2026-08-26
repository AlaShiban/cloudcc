package python

import (
	"sort"
	"strings"

	"github.com/cloudcompiler/cloudcc/internal/lang"
	"github.com/cloudcompiler/cloudcc/internal/source"
	ts "github.com/tree-sitter/go-tree-sitter"
)

var methodCallQuery = source.MustQuery(source.PythonLanguage(),
	`(call function: (attribute object: (identifier) @obj attribute: (identifier) @method)) @call`)

// methodCalls returns every `<object>.<method>(...)` call in the given
// files. Identifier-name matching is deliberate: `from shared.store import
// events` keeps the binding's name, so a name-based scan follows a handle
// across modules without needing a full symbol table.
func methodCalls(files *source.Set, unitFiles []string) []lang.MethodCall {
	var out []lang.MethodCall
	for _, path := range unitFiles {
		f, ok := files.Get(path)
		if !ok || !f.Parsed() {
			continue
		}
		f.Query(methodCallQuery, func(caps map[string]*ts.Node) {
			obj, method, call := caps["obj"], caps["method"], caps["call"]
			if obj == nil || method == nil || call == nil {
				return
			}
			out = append(out, lang.MethodCall{
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

// remoteFunctions returns the module-level functions an entry module offers to
// callers of cloudcc.remote.
//
// Module level only, and public names only: a remote call reaches the module
// the other unit is built around, so a method on a class inside it is no more
// callable from here than a local is. Underscore names are left out for the
// reason they always are -- a unit's private helpers are not its interface, and
// exposing them over the wire by default would make every rename a breaking
// change to somebody else's service.
func remoteFunctions(files *source.Set, entry string) []lang.RemoteFunction {
	f, ok := files.Get(entry)
	if !ok || !f.Parsed() {
		return nil
	}
	root := f.Root()
	if root == nil {
		return nil
	}

	var out []lang.RemoteFunction
	for i := uint(0); i < root.NamedChildCount(); i++ {
		child := root.NamedChild(i)
		// A decorated function is a decorated_definition wrapping the
		// function_definition, which is the shape every framework produces.
		if child.Kind() == "decorated_definition" {
			if def := child.ChildByFieldName("definition"); def != nil {
				child = def
			}
		}
		if child.Kind() != "function_definition" {
			continue
		}
		name := child.ChildByFieldName("name")
		if name == nil {
			continue
		}
		text := f.Text(name)
		if strings.HasPrefix(text, "_") {
			continue
		}
		out = append(out, lang.RemoteFunction{
			Name: text,
			// tree-sitter does not give async functions their own node kind;
			// the `async` keyword is an anonymous child of the definition.
			Async:  isAsyncDef(f, child),
			Offset: int(child.StartByte()),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// isAsyncDef reports whether a function_definition node is `async def`.
func isAsyncDef(f *source.File, def *ts.Node) bool {
	for i := uint(0); i < def.ChildCount(); i++ {
		child := def.Child(i)
		if child.IsNamed() {
			continue
		}
		if f.Text(child) == "async" {
			return true
		}
	}
	return false
}

var identifierQuery = source.MustQuery(source.PythonLanguage(), `(identifier) @id`)

// referencesIdentifier reports whether any of the files mention name.
func referencesIdentifier(files *source.Set, unitFiles []string, name string) bool {
	if name == "" {
		return false
	}
	for _, path := range unitFiles {
		f, ok := files.Get(path)
		if !ok || !f.Parsed() {
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
