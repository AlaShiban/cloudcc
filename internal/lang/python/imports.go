// Package python is the Python frontend: everything the compiler needs to read
// a Python program and nothing it needs to read any other language.
package python

import (
	"path"
	"sort"
	"strings"

	"github.com/cloudcompiler/cloudcc/internal/source"
	ts "github.com/tree-sitter/go-tree-sitter"
)

// pyImport is one static import statement.
type pyImport struct {
	// Module is the dotted module name, empty for `from . import x`.
	Module string
	// Level is the number of leading dots in a relative import.
	Level int
	// Names are the names imported from Module, which may themselves be
	// submodules.
	Names []string
	// Offset is the statement's byte offset, for diagnostics.
	Offset int
	// Relative reports whether the import used leading dots.
	Relative bool
}

// parseImports collects the static import statements in a Python file.
// Dynamic imports (importlib, __import__) are deliberately not followed; the
// closure is defined over static imports only.
func parseImports(f *source.File) []pyImport {
	root := f.Root()
	if root == nil {
		return nil
	}
	var out []pyImport
	var walk func(n *ts.Node)
	walk = func(n *ts.Node) {
		switch n.Kind() {
		case "import_statement":
			for i := uint(0); i < n.NamedChildCount(); i++ {
				child := n.NamedChild(i)
				switch child.Kind() {
				case "dotted_name":
					out = append(out, pyImport{Module: f.Text(child), Offset: int(n.StartByte())})
				case "aliased_import":
					if name := child.ChildByFieldName("name"); name != nil {
						out = append(out, pyImport{Module: f.Text(name), Offset: int(n.StartByte())})
					}
				}
			}
		case "import_from_statement":
			imp := pyImport{Offset: int(n.StartByte())}
			mod := n.ChildByFieldName("module_name")
			if mod != nil {
				text := f.Text(mod)
				imp.Level = leadingDots(text)
				imp.Relative = imp.Level > 0
				imp.Module = strings.TrimLeft(text, ".")
			}
			for i := uint(0); i < n.NamedChildCount(); i++ {
				child := n.NamedChild(i)
				if mod != nil && child.Equals(*mod) {
					continue
				}
				switch child.Kind() {
				case "dotted_name":
					imp.Names = append(imp.Names, f.Text(child))
				case "aliased_import":
					if name := child.ChildByFieldName("name"); name != nil {
						imp.Names = append(imp.Names, f.Text(name))
					}
				case "wildcard_import":
					// `from x import *` still pulls in the module itself.
				}
			}
			out = append(out, imp)
		}
		for i := uint(0); i < n.NamedChildCount(); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(root)
	return out
}

func leadingDots(s string) int {
	n := 0
	for n < len(s) && s[n] == '.' {
		n++
	}
	return n
}

// resolveImport returns the source paths an import statement refers to, out of
// the files present in the set. An import that resolves to nothing is external
// (fastapi, os) or broken; the caller decides which.
func resolveImport(files *source.Set, from string, imp pyImport) []string {
	fromDir := path.Dir(from)
	if fromDir == "." {
		fromDir = ""
	}

	var bases []string
	if imp.Relative {
		// One dot means "this package"; each extra dot climbs one level.
		dir := fromDir
		for i := 1; i < imp.Level; i++ {
			dir = path.Dir(dir)
			if dir == "." {
				dir = ""
			}
		}
		bases = append(bases, dir)
	} else {
		// Absolute imports resolve against the source root, and also against
		// the declaring file's directory, which is how a script run from
		// inside a package still finds its siblings.
		bases = append(bases, "")
		if fromDir != "" {
			bases = append(bases, fromDir)
		}
	}

	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		if _, ok := files.Get(p); ok {
			seen[p] = true
			out = append(out, p)
		}
	}

	for _, base := range bases {
		module := imp.Module
		prefix := module
		if base != "" {
			if prefix == "" {
				prefix = base
			} else {
				prefix = base + "/" + strings.ReplaceAll(module, ".", "/")
			}
		} else if prefix != "" {
			prefix = strings.ReplaceAll(module, ".", "/")
		}

		if prefix != "" {
			for _, cand := range []string{prefix + ".py", prefix + "/__init__.py"} {
				add(cand)
			}
		}
		// `from pkg import mod` and `from . import mod` may name submodules.
		for _, name := range imp.Names {
			sub := name
			if prefix != "" {
				sub = prefix + "/" + name
			}
			for _, cand := range []string{sub + ".py", sub + "/__init__.py"} {
				add(cand)
			}
		}
		// Every package on the path of a resolved module must travel with it,
		// or the import fails at runtime for want of an __init__.py.
		if prefix != "" {
			parts := strings.Split(prefix, "/")
			for i := 1; i <= len(parts); i++ {
				add(strings.Join(parts[:i], "/") + "/__init__.py")
			}
		}
	}
	sort.Strings(out)
	return out
}

// closure returns the transitive local-import closure of entry, skipping any
// path in excluded. unresolved receives relative imports that matched nothing,
// which are the only imports that are unambiguously a local mistake rather
// than an ordinary third-party dependency.
func closure(files *source.Set, entry string, excluded map[string]string) (paths []string, unresolved []pyImport) {
	visited := map[string]bool{}
	queue := []string{entry}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if visited[cur] {
			continue
		}
		f, ok := files.Get(cur)
		if !ok || !f.IsPython() {
			continue
		}
		if _, claimed := excluded[cur]; claimed && cur != entry {
			continue
		}
		visited[cur] = true
		for _, imp := range parseImports(f) {
			targets := resolveImport(files, cur, imp)
			if len(targets) == 0 {
				if imp.Relative {
					up := imp
					up.Offset = imp.Offset
					unresolved = append(unresolved, up)
				}
				continue
			}
			queue = append(queue, targets...)
		}
	}
	for p := range visited {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths, unresolved
}

func renderImport(imp pyImport) string {
	var b strings.Builder
	b.WriteString("from ")
	b.WriteString(strings.Repeat(".", imp.Level))
	b.WriteString(imp.Module)
	if len(imp.Names) > 0 {
		b.WriteString(" import ")
		b.WriteString(strings.Join(imp.Names, ", "))
	}
	return b.String()
}
