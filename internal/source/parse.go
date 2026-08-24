package source

import (
	"fmt"
	"sync"

	ts "github.com/tree-sitter/go-tree-sitter"
	tspython "github.com/tree-sitter/tree-sitter-python/bindings/go"
)

// parsed owns a tree-sitter parse tree. It is a struct rather than a bare
// *ts.Tree so File can carry it without exposing the cgo types to callers that
// do not need them.
type parsed struct {
	tree *ts.Tree
	// language is the tree-sitter grammar the tree was built with, kept so a
	// reparse after a rewrite uses the same one.
	language *ts.Language
	// name is the frontend that owns this file, e.g. "python".
	name string
}

func (f *File) close() {
	if f.tree != nil && f.tree.tree != nil {
		f.tree.tree.Close()
		f.tree.tree = nil
	}
}

// Language returns the frontend that parsed this file, empty when unparsed.
func (f *File) Language() string {
	if f.tree == nil {
		return ""
	}
	return f.tree.name
}

// Parse builds a syntax tree for the file with the given grammar. The language
// name is recorded so a later reparse -- after the shim rewrite splices the
// bytes -- uses the same grammar without the caller having to remember.
func (f *File) Parse(name string, language *ts.Language) error {
	p := ts.NewParser()
	defer p.Close()
	if err := p.SetLanguage(language); err != nil {
		return fmt.Errorf("configuring the %s grammar: %w", name, err)
	}
	tree := p.Parse(f.Content, nil)
	if tree == nil {
		return fmt.Errorf("%s: the %s parser returned no tree", f.Path, name)
	}
	f.close()
	f.tree = &parsed{tree: tree, language: language, name: name}
	return nil
}

var (
	langOnce sync.Once
	pyLang   *ts.Language
)

// PythonLanguage returns the shared tree-sitter Python language handle.
func PythonLanguage() *ts.Language {
	langOnce.Do(func() { pyLang = ts.NewLanguage(tspython.Language()) })
	return pyLang
}

// ParsePython parses src as Python. It is a convenience for the many tests
// that build a Python file directly.
func (f *File) ParsePython() error { return f.Parse("python", PythonLanguage()) }

// Root returns the root node of the file's parse tree.
func (f *File) Root() *ts.Node {
	if f.tree == nil {
		return nil
	}
	return f.tree.tree.RootNode()
}

// SetContent replaces the file's bytes, refreshes its fingerprint, and
// reparses with whatever grammar built the original tree. Reparsing after a
// splice keeps the tree consistent with the bytes, which is what makes
// successive rewrites safe.
func (f *File) SetContent(b []byte) error {
	var name string
	var language *ts.Language
	if f.tree != nil {
		name, language = f.tree.name, f.tree.language
	}
	f.Content = b
	f.SHA256 = Fingerprint(b)
	if language != nil && b != nil {
		return f.Parse(name, language)
	}
	f.close()
	return nil
}

// HasParseError reports whether the tree contains a syntax error.
func (f *File) HasParseError() bool {
	root := f.Root()
	return root != nil && root.HasError()
}

// PositionAt converts a byte offset into a 1-based line and column. Columns
// count bytes, which matches what editors report for ASCII source and is
// adequate for diagnostics.
func (f *File) PositionAt(offset int) (line, col int) {
	line, col = 1, 1
	if offset > len(f.Content) {
		offset = len(f.Content)
	}
	for i := 0; i < offset; i++ {
		if f.Content[i] == '\n' {
			line++
			col = 1
		} else {
			col++
		}
	}
	return line, col
}

// Query runs a tree-sitter query over the file and calls fn for each match.
// Captures are delivered as a name -> node map; a name captured more than once
// in a match keeps the first node.
func (f *File) Query(q *ts.Query, fn func(captures map[string]*ts.Node)) {
	root := f.Root()
	if root == nil {
		return
	}
	qc := ts.NewQueryCursor()
	defer qc.Close()
	matches := qc.Matches(q, root, f.Content)
	names := q.CaptureNames()
	for m := matches.Next(); m != nil; m = matches.Next() {
		caps := make(map[string]*ts.Node, len(m.Captures))
		for i := range m.Captures {
			c := &m.Captures[i]
			name := names[c.Index]
			if _, seen := caps[name]; !seen {
				caps[name] = &c.Node
			}
		}
		fn(caps)
	}
}

// MustQuery compiles a query against a grammar, panicking on a malformed
// pattern. Query patterns are compile-time constants, so a failure is always a
// programming error rather than something to handle.
func MustQuery(language *ts.Language, pattern string) *ts.Query {
	q, err := ts.NewQuery(language, pattern)
	if err != nil {
		panic(fmt.Sprintf("invalid tree-sitter query %q: %v", pattern, err))
	}
	return q
}

// Text returns the source text covered by n.
func (f *File) Text(n *ts.Node) string {
	if n == nil {
		return ""
	}
	return n.Utf8Text(f.Content)
}
