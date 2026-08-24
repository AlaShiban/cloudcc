package python

import (
	"sort"

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
