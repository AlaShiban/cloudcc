package node

import (
	"path"
	"sort"
	"strings"

	"github.com/cloudcompiler/cloudcc/internal/lang"
	"github.com/cloudcompiler/cloudcc/internal/source"
	ts "github.com/tree-sitter/go-tree-sitter"
)

// moduleImport is one static import of another module.
type moduleImport struct {
	// Specifier is the module string as written, e.g. "./store.js".
	Specifier string
	// Offset is the statement's byte offset, for diagnostics.
	Offset int
	// Local reports whether the specifier refers to a file in this program
	// rather than to a package. Only a local one that resolves to nothing is
	// worth warning about; a missing package is npm's business, not ours.
	Local bool
}

// parseImports collects the static imports of a module.
//
// Both module systems are read, because both are idiomatic and a program may
// mix them. Dynamic `import()` and a `require()` with a computed specifier are
// deliberately not followed: the closure is defined over static imports, and
// guessing at a runtime value is how a compiler ships a bundle missing a file.
func parseImports(f *source.File) []moduleImport {
	root := f.Root()
	if root == nil {
		return nil
	}
	var out []moduleImport

	record := func(spec string, offset int) {
		if spec == "" {
			return
		}
		out = append(out, moduleImport{
			Specifier: spec,
			Offset:    offset,
			Local:     strings.HasPrefix(spec, "./") || strings.HasPrefix(spec, "../") || spec == ".",
		})
	}

	var walk func(n *ts.Node)
	walk = func(n *ts.Node) {
		switch n.Kind() {
		case "import_statement", "export_statement":
			// `export { x } from "./y"` reaches another module too.
			if src := n.ChildByFieldName("source"); src != nil {
				record(literalString(f, src), int(n.StartByte()))
			}

		case "call_expression":
			fn := n.ChildByFieldName("function")
			if fn == nil {
				break
			}
			// require("./x"). A dynamic import() is a call_expression whose
			// function is the `import` keyword; it is not followed, because
			// its specifier is usually computed.
			if f.Text(fn) != "require" {
				break
			}
			args := n.ChildByFieldName("arguments")
			if args == nil {
				break
			}
			for i := uint(0); i < args.NamedChildCount(); i++ {
				if spec, ok := stringLiteral(f, args.NamedChild(i)); ok {
					record(spec, int(n.StartByte()))
					break
				}
			}
		}
		for i := uint(0); i < n.NamedChildCount(); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(root)
	return out
}

// candidateSuffixes are tried in order when a specifier has no extension.
//
// Node's own resolution requires the extension under ESM and infers it under
// CommonJS, and TypeScript sources are written importing ".js" that does not
// exist yet. Trying all of them means a program resolves the same way whichever
// convention it follows, which is what a compiler reading both needs.
var candidateSuffixes = []string{
	"", ".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs",
	"/index.ts", "/index.tsx", "/index.js", "/index.jsx", "/index.mjs", "/index.cjs",
}

// resolveImport returns the file a specifier refers to, out of the files
// present in the program.
func resolveImport(files *source.Set, from, specifier string) string {
	if !strings.HasPrefix(specifier, ".") {
		return ""
	}
	base := path.Dir(from)
	target := path.Clean(path.Join(base, specifier))

	// A TypeScript program importing "./store.js" means "./store.ts": the
	// specifier names the file that will exist after compilation.
	rewritten := []string{target}
	for _, ext := range []string{".js", ".mjs", ".cjs"} {
		if strings.HasSuffix(target, ext) {
			stem := strings.TrimSuffix(target, ext)
			rewritten = append(rewritten, stem+".ts", stem+".tsx", stem+".mts", stem+".cts")
		}
	}

	for _, stem := range rewritten {
		for _, suffix := range candidateSuffixes {
			candidate := stem + suffix
			if f, ok := files.Get(candidate); ok && f.Language() == lang.Node {
				return candidate
			}
		}
	}
	return ""
}

// closure returns the transitive local-import closure of entry.
func closure(files *source.Set, entry string, excluded map[string]string) ([]string, []lang.Unresolved) {
	visited := map[string]bool{}
	var unresolved []lang.Unresolved

	queue := []string{entry}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if visited[cur] {
			continue
		}
		f, ok := files.Get(cur)
		if !ok || f.Language() != lang.Node {
			continue
		}
		if _, claimed := excluded[cur]; claimed && cur != entry {
			continue
		}
		visited[cur] = true

		for _, imp := range parseImports(f) {
			target := resolveImport(files, cur, imp.Specifier)
			if target == "" {
				if imp.Local {
					unresolved = append(unresolved, lang.Unresolved{
						Rendered: imp.Specifier,
						Offset:   imp.Offset,
					})
				}
				continue
			}
			queue = append(queue, target)
		}
	}

	paths := make([]string, 0, len(visited))
	for p := range visited {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths, unresolved
}
