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
			// `import type { Pet } from "./store"` reaches nothing at runtime:
			// every TypeScript build erases the whole statement, and esbuild
			// does not put the module in the bundle. Following it anyway pulls
			// that module into the unit -- and if it declares a store, the unit
			// is wired to a store it never touches and granted IAM for it,
			// which quietly costs the least-privilege property the policy
			// derivation exists for.
			//
			// Only the whole-statement form. `import { type Pet, readPet }`
			// still needs the module, and dropping it would leave a bundle
			// missing a file that is genuinely imported.
			if typeOnly(n) {
				break
			}
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

// typeOnly reports an `import type ...` or `export type ...` statement, where
// the `type` keyword sits directly after the leading keyword and so applies to
// the whole statement.
//
// Read positionally rather than by searching for a `type` node anywhere in the
// statement: `import { type Pet, readPet }` contains one too, and that import
// does reach the module.
func typeOnly(n *ts.Node) bool {
	if n.ChildCount() < 2 {
		return false
	}
	return n.Child(1).Kind() == "type"
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

// resolve returns the file a specifier refers to, out of the files present in
// the program, and reports whether the specifier named a local module at all.
//
// A specifier is local when it is relative, or when a tsconfig `paths` pattern
// claims it. The second answer is what makes an alias that resolves to nothing
// a diagnostic rather than silence: without it, `@/store` is indistinguishable
// from an npm package, and the first anyone hears of the missing module is the
// bundler failing to resolve it, several minutes and one abstraction layer away.
func (r *resolver) resolve(from, specifier string) (target string, local bool) {
	var targets []string

	if strings.HasPrefix(specifier, ".") {
		targets = []string{path.Clean(path.Join(path.Dir(from), specifier))}
		local = true
	} else {
		cfg := r.configFor(from)
		aliased, matched := cfg.aliasCandidates(specifier)
		targets, local = aliased, matched
	}

	for _, t := range targets {
		// A TypeScript program importing "./store.js" means "./store.ts": the
		// specifier names the file that will exist after compilation.
		stems := []string{t}
		for _, ext := range []string{".js", ".mjs", ".cjs"} {
			if strings.HasSuffix(t, ext) {
				stem := strings.TrimSuffix(t, ext)
				stems = append(stems, stem+".ts", stem+".tsx", stem+".mts", stem+".cts")
			}
		}
		for _, stem := range stems {
			for _, suffix := range candidateSuffixes {
				candidate := stem + suffix
				if f, ok := r.files.Get(candidate); ok && f.Language() == lang.Node {
					return candidate, local
				}
			}
		}
	}
	return "", local
}

// resolveImport is the single-shot form, for callers that have no closure to
// run and so nothing to cache.
func resolveImport(files *source.Set, from, specifier string) string {
	target, _ := newResolver(files).resolve(from, specifier)
	return target
}

// closure returns the transitive local-import closure of entry.
func closure(files *source.Set, entry string, excluded map[string]string) ([]string, []lang.Unresolved) {
	visited := map[string]bool{}
	var unresolved []lang.Unresolved

	res := newResolver(files)
	// Read up front rather than on the first alias. A tsconfig this compiler
	// cannot parse is a fact about the project whether or not today's imports
	// happen to need it -- and if it is only noticed when an alias is added,
	// the report arrives attached to the wrong change.
	res.configFor(entry)

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
			target, local := res.resolve(cur, imp.Specifier)
			if target == "" {
				if local {
					u := lang.Unresolved{Rendered: imp.Specifier, Offset: imp.Offset}
					if !imp.Local {
						// It was not relative, so a tsconfig pattern is what
						// claimed it. Say which, or the reader is left looking
						// for a file called "@/store".
						u.Why = "a tsconfig `paths` pattern maps it, but nothing it maps to is a file in this tree"
					}
					unresolved = append(unresolved, u)
				}
				continue
			}
			queue = append(queue, target)
		}
	}

	// Read once, reported once. A configuration this compiler could not read is
	// not a per-import problem, and repeating it for every alias in the file
	// would bury the one line that matters.
	for _, p := range sortedKeys(res.problems) {
		unresolved = append(unresolved, lang.Unresolved{Rendered: p, Why: res.problems[p]})
	}

	paths := make([]string, 0, len(visited))
	for p := range visited {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths, unresolved
}
