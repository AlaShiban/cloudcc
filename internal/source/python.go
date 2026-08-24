package source

import (
	"fmt"
	"sync"

	ts "github.com/tree-sitter/go-tree-sitter"
	tspython "github.com/tree-sitter/tree-sitter-python/bindings/go"
)

// pyTree owns a tree-sitter parse tree. It is a struct rather than a bare
// *ts.Tree so File can carry it without exposing the cgo types to callers that
// do not need them.
type pyTree struct {
	tree *ts.Tree
}

func (f *File) close() {
	if f.tree != nil && f.tree.tree != nil {
		f.tree.tree.Close()
		f.tree.tree = nil
	}
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

// ParsePython parses src as Python and stores the tree on f.
func (f *File) ParsePython() error {
	p := ts.NewParser()
	defer p.Close()
	if err := p.SetLanguage(PythonLanguage()); err != nil {
		return fmt.Errorf("configuring the Python grammar: %w", err)
	}
	tree := p.Parse(f.Content, nil)
	if tree == nil {
		return fmt.Errorf("%s: the Python parser returned no tree", f.Path)
	}
	f.close()
	f.tree = &pyTree{tree: tree}
	return nil
}

// Root returns the root node of the file's parse tree.
func (f *File) Root() *ts.Node {
	if f.tree == nil {
		return nil
	}
	return f.tree.tree.RootNode()
}

// SetContent replaces the file's bytes, refreshes its fingerprint, and
// reparses when the file is Python. Reparsing after a splice keeps the AST
// consistent with the bytes, which is what makes successive rewrites safe.
func (f *File) SetContent(b []byte) error {
	wasPython := f.IsPython()
	f.Content = b
	f.SHA256 = Fingerprint(b)
	if wasPython {
		return f.ParsePython()
	}
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

// MustQuery compiles a query against the Python grammar, panicking on a
// malformed pattern. Query patterns are compile-time constants, so a failure
// is always a programming error.
func MustQuery(pattern string) *ts.Query {
	q, err := ts.NewQuery(PythonLanguage(), pattern)
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
