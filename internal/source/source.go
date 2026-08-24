// Package source turns a directory tree into the parsed source set the
// compiler works on. Python files are parsed eagerly with tree-sitter; every
// other file is carried through as opaque bytes.
package source

import (
	"crypto/sha256"
	"encoding/hex"
	"path"
	"sort"
	"strings"
)

// File is one input file. Content is the authoritative bytes; SHA256 is
// recorded at ingest so a future incremental mode has fingerprints to work
// with (D8).
type File struct {
	// Path is relative to the source root, always slash-separated.
	Path    string
	Content []byte
	SHA256  string

	// tree is the parsed tree-sitter tree for Python files, nil otherwise.
	tree *pyTree
}

// IsPython reports whether the file was parsed as Python.
func (f *File) IsPython() bool { return f.tree != nil }

// Fingerprint computes the SHA-256 of b as lowercase hex.
func Fingerprint(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Set is the collection of input files, keyed by relative path.
type Set struct {
	// Root is the absolute path of the source directory.
	Root string
	// RequirementsPath is the relative path of the discovered
	// requirements.txt, if any.
	RequirementsPath string
	// PyProjectPath is the relative path of the discovered pyproject.toml, if
	// any.
	PyProjectPath string

	files map[string]*File
}

// NewSet returns an empty set rooted at root.
func NewSet(root string) *Set {
	return &Set{Root: root, files: map[string]*File{}}
}

// Add inserts or replaces a file.
func (s *Set) Add(f *File) {
	if s.files == nil {
		s.files = map[string]*File{}
	}
	s.files[f.Path] = f
}

// Get returns the file at the given relative path.
func (s *Set) Get(p string) (*File, bool) {
	f, ok := s.files[p]
	return f, ok
}

// Remove deletes a file from the set, releasing any parse tree it held.
func (s *Set) Remove(p string) {
	if f, ok := s.files[p]; ok {
		f.close()
		delete(s.files, p)
	}
}

// Len returns the file count.
func (s *Set) Len() int { return len(s.files) }

// Paths returns every relative path in sorted order.
func (s *Set) Paths() []string {
	out := make([]string, 0, len(s.files))
	for p := range s.files {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Files returns every file in sorted-path order.
func (s *Set) Files() []*File {
	paths := s.Paths()
	out := make([]*File, 0, len(paths))
	for _, p := range paths {
		out = append(out, s.files[p])
	}
	return out
}

// PythonFiles returns the parsed Python files in sorted-path order.
func (s *Set) PythonFiles() []*File {
	var out []*File
	for _, f := range s.Files() {
		if f.IsPython() {
			out = append(out, f)
		}
	}
	return out
}

// Close releases every parse tree in the set.
func (s *Set) Close() {
	for _, f := range s.files {
		f.close()
	}
}

// ModuleName converts a relative file path to its Python dotted module name.
// A package's __init__.py maps to the package itself.
func ModuleName(rel string) string {
	rel = strings.TrimSuffix(rel, ".py")
	if base := path.Base(rel); base == "__init__" {
		rel = path.Dir(rel)
		if rel == "." {
			return ""
		}
	}
	return strings.ReplaceAll(rel, "/", ".")
}

// ModulePaths returns the candidate file paths a dotted module name could
// resolve to, in the order they should be tried.
func ModulePaths(module string) []string {
	p := strings.ReplaceAll(module, ".", "/")
	return []string{p + ".py", p + "/__init__.py"}
}
