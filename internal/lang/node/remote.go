package node

import (
	"sort"
	"strings"

	"github.com/cloudcompiler/cloudcc/internal/lang"
	"github.com/cloudcompiler/cloudcc/internal/source"
	ts "github.com/tree-sitter/go-tree-sitter"
)

// remoteFunctions returns the functions an entry module exports to callers of
// cloudcc.remote.
//
// Exported and top-level only: a remote call reaches the module the other unit
// is built around, so something it does not export is no more callable from
// another unit than a local is. Underscore names are left out for the reason
// they always are -- a unit's private helpers are not its interface.
//
// The AST is walked rather than queried because the shapes wanted here are
// three sibling node kinds, and a query would have to be compiled for each of
// the four grammars this frontend supports.
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
	add := func(name string, async bool, offset int) {
		if name == "" || strings.HasPrefix(name, "_") {
			return
		}
		out = append(out, lang.RemoteFunction{Name: name, Async: async, Offset: offset})
	}

	for i := uint(0); i < root.NamedChildCount(); i++ {
		stmt := root.NamedChild(i)
		if stmt.Kind() != "export_statement" {
			continue
		}
		decl := stmt.ChildByFieldName("declaration")
		if decl == nil {
			continue
		}
		switch decl.Kind() {
		case "function_declaration", "generator_function_declaration":
			// export [async] function name(...) {}
			if name := decl.ChildByFieldName("name"); name != nil {
				add(f.Text(name), hasAsyncKeyword(f, decl), int(stmt.StartByte()))
			}
		case "lexical_declaration", "variable_declaration":
			// export const name = async (...) => {}
			for j := uint(0); j < decl.NamedChildCount(); j++ {
				d := decl.NamedChild(j)
				if d.Kind() != "variable_declarator" {
					continue
				}
				name := d.ChildByFieldName("name")
				value := d.ChildByFieldName("value")
				if name == nil || value == nil {
					continue
				}
				switch value.Kind() {
				case "arrow_function", "function_expression", "function":
					add(f.Text(name), hasAsyncKeyword(f, value), int(stmt.StartByte()))
				}
			}
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// hasAsyncKeyword reports whether a function node carries `async`. Tree-sitter
// gives it no node kind of its own; it is an anonymous child.
func hasAsyncKeyword(f *source.File, fn *ts.Node) bool {
	for i := uint(0); i < fn.ChildCount(); i++ {
		child := fn.Child(i)
		if !child.IsNamed() && f.Text(child) == "async" {
			return true
		}
	}
	return false
}

// importBindingSpans returns the byte ranges to delete so that none of names is
// bound by an import in f.
//
// A module reached through cloudcc.remote is another unit's entry module, and
// this unit's bundle deliberately does not carry it, so the import that made
// the uncompiled program work would be the first line to fail.
//
// A whole statement goes when it bound nothing else; otherwise only the one
// specifier does, because `import { a, b } from "./x.js"` is one statement
// serving two purposes and only one of them has moved over the wire.
func importBindingSpans(f *source.File, names map[string]bool) [][2]int {
	if len(names) == 0 {
		return nil
	}
	root := f.Root()
	if root == nil {
		return nil
	}

	var spans [][2]int
	for i := uint(0); i < root.NamedChildCount(); i++ {
		stmt := root.NamedChild(i)
		if stmt.Kind() != "import_statement" {
			continue
		}
		bindings := importBindings(stmt)
		if len(bindings) == 0 {
			continue
		}
		var matched []*ts.Node
		for _, b := range bindings {
			if names[f.Text(b.name)] {
				matched = append(matched, b.node)
			}
		}
		if len(matched) == 0 {
			continue
		}
		if len(matched) == len(bindings) {
			start, end := int(stmt.StartByte()), int(stmt.EndByte())
			if end < len(f.Content) && f.Content[end] == ';' {
				end++
			}
			if end < len(f.Content) && f.Content[end] == '\n' {
				end++
			}
			spans = append(spans, [2]int{start, end})
			continue
		}
		for _, node := range matched {
			spans = append(spans, specifierSpan(f, node))
		}
	}
	return spans
}

// binding is one name an import statement introduces, and the node to remove
// if only that name has to go.
type binding struct {
	name *ts.Node
	node *ts.Node
}

// importBindings returns every local name an import statement binds.
func importBindings(stmt *ts.Node) []binding {
	var out []binding
	var walk func(n *ts.Node)
	walk = func(n *ts.Node) {
		switch n.Kind() {
		case "import_specifier":
			// { a } or { a as b } -- the local name is the alias when present.
			if alias := n.ChildByFieldName("alias"); alias != nil {
				out = append(out, binding{alias, n})
				return
			}
			if name := n.ChildByFieldName("name"); name != nil {
				out = append(out, binding{name, n})
			}
			return
		case "namespace_import":
			// * as pricingModule
			for i := uint(0); i < n.NamedChildCount(); i++ {
				if child := n.NamedChild(i); child.Kind() == "identifier" {
					out = append(out, binding{child, n})
				}
			}
			return
		case "identifier":
			// A default import sits directly under import_clause.
			if parent := n.Parent(); parent != nil && parent.Kind() == "import_clause" {
				out = append(out, binding{n, n})
			}
			return
		}
		for i := uint(0); i < n.NamedChildCount(); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(stmt)
	return out
}

// specifierSpan is one import specifier plus the separator that would
// otherwise be left dangling.
func specifierSpan(f *source.File, node *ts.Node) [2]int {
	start, end := int(node.StartByte()), int(node.EndByte())
	for end < len(f.Content) && (f.Content[end] == ' ' || f.Content[end] == ',') {
		wasComma := f.Content[end] == ','
		end++
		if wasComma {
			for end < len(f.Content) && f.Content[end] == ' ' {
				end++
			}
			return [2]int{start, end}
		}
	}
	end = int(node.EndByte())
	for start > 0 && (f.Content[start-1] == ' ' || f.Content[start-1] == ',') {
		start--
	}
	return [2]int{start, end}
}
