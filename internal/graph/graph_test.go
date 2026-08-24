package graph

import (
	"reflect"
	"strings"
	"testing"
)

func build(edges [][2]string, extra ...string) *Directed[string] {
	g := New[string]()
	for _, e := range edges {
		g.Add(e[0], e[0])
		g.Add(e[1], e[1])
	}
	for _, n := range extra {
		g.Add(n, n)
	}
	for _, e := range edges {
		g.Connect(e[0], e[1])
	}
	return g
}

func TestTopoSortRespectsEdgesAndBreaksTiesAlphabetically(t *testing.T) {
	g := build([][2]string{{"b", "c"}, {"a", "c"}}, "z")
	got, err := g.TopoSort()
	if err != nil {
		t.Fatal(err)
	}
	// Tie-break is "smallest ready key first": c becomes ready once b is
	// emitted, and sorts before the isolated node z.
	want := []string{"a", "b", "c", "z"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("TopoSort = %v, want %v", got, want)
	}
}

func TestTopoSortIsStableAcrossRuns(t *testing.T) {
	var first []string
	for i := 0; i < 50; i++ {
		g := build([][2]string{{"d", "e"}, {"a", "e"}, {"c", "d"}, {"b", "d"}})
		got, err := g.TopoSort()
		if err != nil {
			t.Fatal(err)
		}
		if first == nil {
			first = got
		} else if !reflect.DeepEqual(got, first) {
			t.Fatalf("run %d gave %v, first run gave %v", i, got, first)
		}
	}
}

func TestTopoSortDetectsCycle(t *testing.T) {
	g := build([][2]string{{"a", "b"}, {"b", "c"}, {"c", "a"}})
	_, err := g.TopoSort()
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("expected a cycle error, got %v", err)
	}
	for _, n := range []string{"a", "b", "c"} {
		if !strings.Contains(err.Error(), n) {
			t.Errorf("cycle error should name %q: %v", n, err)
		}
	}
}

func TestEdgesAreASetAndSorted(t *testing.T) {
	g := build([][2]string{{"a", "z"}, {"a", "b"}, {"a", "z"}})
	if got, want := g.Successors("a"), []string{"b", "z"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Successors = %v, want %v", got, want)
	}
	if got, want := g.Predecessors("z"), []string{"a"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Predecessors = %v, want %v", got, want)
	}
}

func TestSuccessorsCopyIsIndependent(t *testing.T) {
	g := build([][2]string{{"a", "b"}, {"a", "c"}})
	s := g.Successors("a")
	s[0] = "mutated"
	if g.Successors("a")[0] != "b" {
		t.Error("Successors returned an aliased slice")
	}
}

func TestConnectMissingEndpointPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected a panic for a dangling edge")
		}
	}()
	g := New[string]()
	g.Add("a", "a")
	g.Connect("a", "nope")
}
