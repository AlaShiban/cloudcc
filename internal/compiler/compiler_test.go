package compiler

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/cloudcompiler/cloudcc/internal/config"
	"github.com/cloudcompiler/cloudcc/internal/source"
	"github.com/spf13/afero"
)

type fake struct {
	name string
	deps []string
	run  func(*Context) error
}

func (f fake) Name() string   { return f.name }
func (f fake) Deps() []string { return f.deps }
func (f fake) Transform(c *Context) error {
	if f.run != nil {
		return f.run(c)
	}
	return nil
}

func TestScheduleOrdersByDeclaredDeps(t *testing.T) {
	var plugins []Plugin
	for _, f := range []fake{
		{name: "render", deps: []string{"resolve"}},
		{name: "resolve", deps: []string{"persist", "expose"}},
		{name: "persist", deps: []string{"detect"}},
		{name: "expose", deps: []string{"detect"}},
		{name: "detect"},
	} {
		plugins = append(plugins, f)
	}

	ordered, err := Schedule(plugins)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range ordered {
		names = append(names, p.Name())
	}
	// detect first; expose before persist only because of the alphabetical
	// tie-break; render last.
	want := []string{"detect", "expose", "persist", "resolve", "render"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("order = %v, want %v", names, want)
	}
}

func TestScheduleIsDeterministic(t *testing.T) {
	var first []string
	for i := 0; i < 50; i++ {
		plugins := []Plugin{
			fake{name: "e", deps: []string{"a"}},
			fake{name: "d", deps: []string{"a"}},
			fake{name: "c", deps: []string{"a"}},
			fake{name: "b", deps: []string{"a"}},
			fake{name: "a"},
		}
		ordered, err := Schedule(plugins)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, p := range ordered {
			names = append(names, p.Name())
		}
		if first == nil {
			first = names
		} else if !reflect.DeepEqual(names, first) {
			t.Fatalf("run %d gave %v, first run gave %v", i, names, first)
		}
	}
}

func TestScheduleRejectsUnknownDependency(t *testing.T) {
	_, err := Schedule([]Plugin{fake{name: "a", deps: []string{"ghost"}}})
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("expected an unknown-dependency error, got %v", err)
	}
}

func TestScheduleRejectsCycle(t *testing.T) {
	_, err := Schedule([]Plugin{
		fake{name: "a", deps: []string{"b"}},
		fake{name: "b", deps: []string{"a"}},
	})
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("expected a cycle error, got %v", err)
	}
}

func TestScheduleRejectsDuplicateNames(t *testing.T) {
	_, err := Schedule([]Plugin{fake{name: "a"}, fake{name: "a"}})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected a duplicate-name error, got %v", err)
	}
}

func TestCompileRunsInOrderAndWrapsErrors(t *testing.T) {
	var ran []string
	record := func(n string) func(*Context) error {
		return func(*Context) error { ran = append(ran, n); return nil }
	}
	boom := errors.New("boom")

	c, err := NewCompiler([]Plugin{
		fake{name: "second", deps: []string{"first"}, run: func(*Context) error { return boom }},
		fake{name: "first", run: record("first")},
		fake{name: "third", deps: []string{"second"}, run: record("third")},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = c.Compile(newTestContext(t))
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want it to wrap boom", err)
	}
	if !strings.HasPrefix(err.Error(), "second: ") {
		t.Errorf("error should name the failing plugin: %q", err)
	}
	if !reflect.DeepEqual(ran, []string{"first"}) {
		t.Errorf("ran = %v; the chain should stop at the failure", ran)
	}
}

func newTestContext(t *testing.T) *Context {
	t.Helper()
	cfg := config.New()
	cfg.App = "test"
	return NewContext(cfg, t.TempDir(), afero.NewMemMapFs())
}

func TestUnitsForListsEveryUnitSharingAFile(t *testing.T) {
	ctx := newTestContext(t)
	ctx.UnitFiles = map[string][]string{
		"worker": {"shared/db.py", "worker.py"},
		"api":    {"api.py", "shared/db.py"},
	}
	if got, want := ctx.UnitsFor("shared/db.py"), []string{"api", "worker"}; !reflect.DeepEqual(got, want) {
		t.Errorf("UnitsFor = %v, want %v", got, want)
	}
	if got, want := ctx.UnitsFor("worker.py"), []string{"worker"}; !reflect.DeepEqual(got, want) {
		t.Errorf("UnitsFor = %v, want %v", got, want)
	}
	if got := ctx.UnitsFor("nowhere.py"); got != nil {
		t.Errorf("UnitsFor = %v, want nil", got)
	}
}

func TestPosResolvesAgainstFileContent(t *testing.T) {
	ctx := newTestContext(t)
	ctx.Files.Add(&source.File{Path: "a.py", Content: []byte("one\ntwo\n")})
	pos := ctx.Pos("a.py", 5)
	if pos.Line != 2 || pos.Col != 2 || pos.File != "a.py" {
		t.Errorf("Pos = %+v", pos)
	}
}
