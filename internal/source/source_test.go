package source

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	ts "github.com/tree-sitter/go-tree-sitter"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestWalkCollectsAndSkips(t *testing.T) {
	root := writeTree(t, map[string]string{
		"app.py":                      "x = 1\n",
		"pkg/__init__.py":             "",
		"pkg/helper.py":               "def f(): pass\n",
		"static/index.html":           "<html></html>",
		"requirements.txt":            "fastapi\n",
		".venv/lib/thing.py":          "should not appear",
		"node_modules/a/index.js":     "nope",
		"__pycache__/app.cpython.pyc": "nope",
		"compiled/index.ts":           "previous output",
	})

	set, err := Walk(Options{Root: root, SkipPaths: []string{"compiled"}})
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()

	want := []string{"app.py", "pkg/__init__.py", "pkg/helper.py", "requirements.txt", "static/index.html"}
	if got := set.Paths(); !reflect.DeepEqual(got, want) {
		t.Errorf("Paths() = %v, want %v", got, want)
	}
	if set.RequirementsPath != "requirements.txt" {
		t.Errorf("RequirementsPath = %q", set.RequirementsPath)
	}
}

func TestWalkParsesPythonOnly(t *testing.T) {
	root := writeTree(t, map[string]string{
		"app.py":    "import os\n",
		"notes.txt": "hello",
	})
	set, err := Walk(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()

	py, _ := set.Get("app.py")
	if !py.IsPython() || py.Root() == nil {
		t.Error("app.py was not parsed")
	}
	txt, _ := set.Get("notes.txt")
	if txt.IsPython() {
		t.Error("notes.txt should not be parsed as Python")
	}
	if got := len(set.PythonFiles()); got != 1 {
		t.Errorf("PythonFiles() has %d entries, want 1", got)
	}
}

func TestFingerprintsRecordedAtIngest(t *testing.T) {
	root := writeTree(t, map[string]string{"a.py": "x = 1\n"})
	set, err := Walk(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()
	f, _ := set.Get("a.py")
	want := Fingerprint([]byte("x = 1\n"))
	if f.SHA256 != want {
		t.Errorf("SHA256 = %q, want %q", f.SHA256, want)
	}
	if len(f.SHA256) != 64 {
		t.Errorf("fingerprint is not a hex sha256: %q", f.SHA256)
	}
}

func TestWalkRejectsNonDirectory(t *testing.T) {
	root := writeTree(t, map[string]string{"a.py": ""})
	if _, err := Walk(Options{Root: filepath.Join(root, "a.py")}); err == nil {
		t.Fatal("expected an error when the source path is a file")
	}
}

func TestSetContentReparses(t *testing.T) {
	f := &File{Path: "a.py", Content: []byte("x = 1\n")}
	if err := f.ParsePython(); err != nil {
		t.Fatal(err)
	}
	defer f.close()
	before := f.SHA256

	if err := f.SetContent([]byte("def g():\n    return 2\n")); err != nil {
		t.Fatal(err)
	}
	if f.SHA256 == before {
		t.Error("SetContent did not refresh the fingerprint")
	}
	if f.Root() == nil || f.Root().ChildCount() == 0 {
		t.Error("SetContent did not reparse")
	}
	if f.HasParseError() {
		t.Error("valid Python reported a parse error")
	}
}

func TestPositionAt(t *testing.T) {
	f := &File{Path: "a.py", Content: []byte("one\ntwo\nthree\n")}
	cases := []struct{ off, line, col int }{
		{0, 1, 1}, {2, 1, 3}, {4, 2, 1}, {8, 3, 1}, {999, 4, 1},
	}
	for _, c := range cases {
		line, col := f.PositionAt(c.off)
		if line != c.line || col != c.col {
			t.Errorf("PositionAt(%d) = %d:%d, want %d:%d", c.off, line, col, c.line, c.col)
		}
	}
}

func TestModuleName(t *testing.T) {
	cases := map[string]string{
		"app.py":            "app",
		"pkg/helper.py":     "pkg.helper",
		"pkg/__init__.py":   "pkg",
		"a/b/c/__init__.py": "a.b.c",
		"__init__.py":       "",
	}
	for in, want := range cases {
		if got := ModuleName(in); got != want {
			t.Errorf("ModuleName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestModulePaths(t *testing.T) {
	want := []string{"pkg/helper.py", "pkg/helper/__init__.py"}
	if got := ModulePaths("pkg.helper"); !reflect.DeepEqual(got, want) {
		t.Errorf("ModulePaths = %v, want %v", got, want)
	}
}

func TestQueryFindsCalls(t *testing.T) {
	f := &File{Path: "a.py", Content: []byte("import cloudcompiler as cc\npets = cc.persist_kv(\"petsByOwner\")\n")}
	if err := f.ParsePython(); err != nil {
		t.Fatal(err)
	}
	defer f.close()

	q := MustQuery(`(call function: (_) @fn) @call`)
	defer q.Close()

	var fns []string
	f.Query(q, func(caps map[string]*ts.Node) {
		fns = append(fns, f.Text(caps["fn"]))
	})
	if !reflect.DeepEqual(fns, []string{"cc.persist_kv"}) {
		t.Errorf("query captured %v, want [cc.persist_kv]", fns)
	}
}

func TestMustQueryPanicsOnBadPattern(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected a panic for an invalid query")
		}
	}()
	MustQuery(`(this_node_type_does_not_exist)`)
}
