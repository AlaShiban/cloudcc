// Package graph is a small deterministic directed graph over string-keyed
// nodes. It backs both the plugin scheduler and the compiler's IR program.
//
// Every traversal iterates in sorted key order, so callers get byte-stable
// output without sprinkling sort calls through generation code (D18).
package graph

import (
	"fmt"
	"sort"
)

// Directed is a directed graph whose nodes are values of type N keyed by
// string. Duplicate keys replace the existing node.
type Directed[N any] struct {
	nodes map[string]N
	order map[string][]string // key -> sorted successor keys
	rev   map[string][]string // key -> sorted predecessor keys
}

// New returns an empty graph.
func New[N any]() *Directed[N] {
	return &Directed[N]{
		nodes: map[string]N{},
		order: map[string][]string{},
		rev:   map[string][]string{},
	}
}

// Add inserts or replaces the node stored at key.
func (g *Directed[N]) Add(key string, n N) {
	g.nodes[key] = n
	if _, ok := g.order[key]; !ok {
		g.order[key] = nil
		g.rev[key] = nil
	}
}

// Get returns the node at key.
func (g *Directed[N]) Get(key string) (N, bool) {
	n, ok := g.nodes[key]
	return n, ok
}

// Has reports whether key is present.
func (g *Directed[N]) Has(key string) bool {
	_, ok := g.nodes[key]
	return ok
}

// Len returns the node count.
func (g *Directed[N]) Len() int { return len(g.nodes) }

// Keys returns every node key in sorted order.
func (g *Directed[N]) Keys() []string {
	keys := make([]string, 0, len(g.nodes))
	for k := range g.nodes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Nodes returns every node in sorted-key order.
func (g *Directed[N]) Nodes() []N {
	keys := g.Keys()
	out := make([]N, 0, len(keys))
	for _, k := range keys {
		out = append(out, g.nodes[k])
	}
	return out
}

// Connect records a from -> to edge. Both endpoints must already exist as
// nodes; connecting a missing endpoint is a programming error and panics,
// because a dangling edge always indicates a plugin bug rather than bad input.
func (g *Directed[N]) Connect(from, to string) {
	if !g.Has(from) || !g.Has(to) {
		panic(fmt.Sprintf("graph.Connect: missing endpoint (%q -> %q)", from, to))
	}
	g.order[from] = insertSorted(g.order[from], to)
	g.rev[to] = insertSorted(g.rev[to], from)
}

// Successors returns the sorted keys of nodes that key points at.
func (g *Directed[N]) Successors(key string) []string { return append([]string(nil), g.order[key]...) }

// Predecessors returns the sorted keys of nodes that point at key.
func (g *Directed[N]) Predecessors(key string) []string { return append([]string(nil), g.rev[key]...) }

// TopoSort returns node keys ordered so that every node appears after all of
// its predecessors, breaking ties alphabetically for determinism. A cycle is
// reported as an error naming the nodes still unresolved.
func (g *Directed[N]) TopoSort() ([]string, error) {
	indeg := map[string]int{}
	for _, k := range g.Keys() {
		indeg[k] = len(g.rev[k])
	}
	var ready []string
	for _, k := range g.Keys() {
		if indeg[k] == 0 {
			ready = append(ready, k)
		}
	}
	sort.Strings(ready)

	var out []string
	for len(ready) > 0 {
		k := ready[0]
		ready = ready[1:]
		out = append(out, k)
		for _, succ := range g.order[k] {
			indeg[succ]--
			if indeg[succ] == 0 {
				ready = insertSorted(ready, succ)
			}
		}
	}
	if len(out) != len(g.nodes) {
		var stuck []string
		for _, k := range g.Keys() {
			if indeg[k] > 0 {
				stuck = append(stuck, k)
			}
		}
		return nil, fmt.Errorf("cycle detected among: %v", stuck)
	}
	return out, nil
}

func insertSorted(s []string, v string) []string {
	i := sort.SearchStrings(s, v)
	if i < len(s) && s[i] == v {
		return s // already present; edges are a set, not a multiset
	}
	s = append(s, "")
	copy(s[i+1:], s[i:])
	s[i] = v
	return s
}
