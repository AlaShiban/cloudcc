// Package ir defines the two-layer intermediate representation every plugin
// shares (D7).
//
// Layer 1 -- Intents -- are provider-agnostic: "a KV store named petsByOwner".
// Only capability plugins create them.
//
// Layer 2 -- Resources -- are provider-typed: "an aws.dynamodb.Table".
// Only the provider resolver creates them.
//
// The two layers live in one Program connected by `resolves_to` edges, so the
// expansion is inspectable via --dump-ir. Keeping the layers separate is the
// point of the architecture; do not let capability plugins reach for Resources.
package ir

import (
	"fmt"
	"sort"

	"github.com/cloudcompiler/cloudcc/internal/config"
	"github.com/cloudcompiler/cloudcc/internal/graph"
)

// Key identifies a node. Kind is a capability name for intents
// ("persist_kv") and a provider-qualified type for resources ("aws.dynamodb").
type Key struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

func (k Key) String() string { return k.Kind + "/" + k.ID }

// IsZero reports whether k is unset.
func (k Key) IsZero() bool { return k.Kind == "" && k.ID == "" }

// Edge kinds.
const (
	// EdgeDependsOn is a plain ordering/creation dependency.
	EdgeDependsOn = "depends_on"
	// EdgeUses means the source (an execution unit) reads or writes the target.
	// IAM policies and environment wiring are both derived from these.
	EdgeUses = "uses"
	// EdgeExposes connects a gateway intent to the unit it fronts.
	EdgeExposes = "exposes"
	// EdgeResolvesTo connects an intent to a concrete resource it expanded into.
	EdgeResolvesTo = "resolves_to"
	// EdgePublishes / EdgeSubscribes connect execution units to topics.
	EdgePublishes  = "publishes"
	EdgeSubscribes = "subscribes"
)

// Intent is a provider-agnostic capability node.
type Intent interface {
	Key() Key
	Capability() string
	// Configure applies the layered configuration result for this node.
	Configure(cfg config.ResourceConfig) error
	// Config returns the configuration previously applied.
	Config() config.ResourceConfig
}

// Resource is a provider-typed node destined for the IaC backend.
type Resource interface {
	Key() Key
	// Template names the entry in the IaC backend's resource template registry.
	Template() string
	// Props are the resource arguments, before pulumi_params are merged.
	Props() map[string]any
	// EnvOutputs maps environment variable names to the value an execution
	// unit should receive, e.g. CLOUDCC_KV_PETSBYOWNER_TABLE -> this table's name.
	//
	// PLAN-DEVIATION: the brief types this as map[string]string (env name ->
	// property name). A plain property name cannot express an RDS connection
	// URL, which is assembled from several outputs, so a binding is either a
	// property of this resource or an arbitrary expression.
	EnvOutputs() map[string]EnvBinding
}

// Edge is a typed connection between two nodes.
type Edge struct {
	From Key    `json:"from"`
	To   Key    `json:"to"`
	Kind string `json:"kind"`
}

func edgeKey(e Edge) string { return e.Kind + "|" + e.From.String() + "|" + e.To.String() }

// Program is the whole compiled model: both node layers plus their edges.
type Program struct {
	intents   *graph.Directed[Intent]
	resources *graph.Directed[Resource]
	edges     map[string]Edge
}

// NewProgram returns an empty Program.
func NewProgram() *Program {
	return &Program{
		intents:   graph.New[Intent](),
		resources: graph.New[Resource](),
		edges:     map[string]Edge{},
	}
}

// AddIntent inserts an intent. Re-adding the same key is a no-op that returns
// the existing node, which lets several plugins converge on one shared
// resource (two execution units using one persist_kv id, for instance).
func (p *Program) AddIntent(in Intent) Intent {
	if existing, ok := p.intents.Get(in.Key().String()); ok {
		return existing
	}
	p.intents.Add(in.Key().String(), in)
	return in
}

// Intent returns the intent at key.
func (p *Program) Intent(k Key) (Intent, bool) { return p.intents.Get(k.String()) }

// Intents returns every intent in sorted-key order.
func (p *Program) Intents() []Intent { return p.intents.Nodes() }

// IntentsOfKind returns the intents whose Key.Kind is kind, sorted by key.
func (p *Program) IntentsOfKind(kind string) []Intent {
	var out []Intent
	for _, in := range p.intents.Nodes() {
		if in.Key().Kind == kind {
			out = append(out, in)
		}
	}
	return out
}

// AddResource inserts a concrete resource, replacing any resource at the same
// key.
func (p *Program) AddResource(r Resource) Resource {
	if existing, ok := p.resources.Get(r.Key().String()); ok {
		return existing
	}
	p.resources.Add(r.Key().String(), r)
	return r
}

// Resource returns the resource at key.
func (p *Program) Resource(k Key) (Resource, bool) { return p.resources.Get(k.String()) }

// Resources returns every resource in sorted-key order.
func (p *Program) Resources() []Resource { return p.resources.Nodes() }

// Connect records an edge. Edges are a set: recording the same triple twice is
// a no-op.
func (p *Program) Connect(from, to Key, kind string) {
	e := Edge{From: from, To: to, Kind: kind}
	p.edges[edgeKey(e)] = e
}

// Edges returns every edge in a stable order.
func (p *Program) Edges() []Edge {
	keys := make([]string, 0, len(p.edges))
	for k := range p.edges {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]Edge, 0, len(keys))
	for _, k := range keys {
		out = append(out, p.edges[k])
	}
	return out
}

// EdgesFrom returns the edges leaving from with the given kind ("" matches any
// kind), in stable order.
func (p *Program) EdgesFrom(from Key, kind string) []Edge {
	var out []Edge
	for _, e := range p.Edges() {
		if e.From == from && (kind == "" || e.Kind == kind) {
			out = append(out, e)
		}
	}
	return out
}

// EdgesTo returns the edges arriving at to with the given kind ("" matches any
// kind), in stable order.
func (p *Program) EdgesTo(to Key, kind string) []Edge {
	var out []Edge
	for _, e := range p.Edges() {
		if e.To == to && (kind == "" || e.Kind == kind) {
			out = append(out, e)
		}
	}
	return out
}

// ResolvedFrom returns the concrete resources an intent expanded into, in
// stable order.
func (p *Program) ResolvedFrom(intent Key) []Resource {
	var out []Resource
	for _, e := range p.EdgesFrom(intent, EdgeResolvesTo) {
		if r, ok := p.Resource(e.To); ok {
			out = append(out, r)
		}
	}
	return out
}

// Resolve records that intent expanded into r, adding r if necessary.
func (p *Program) Resolve(intent Key, r Resource) Resource {
	added := p.AddResource(r)
	p.Connect(intent, added.Key(), EdgeResolvesTo)
	return added
}

// ResourceOrder returns resource keys in dependency order: a resource appears
// after every resource it depends on. Dependencies between resources come from
// depends_on and uses edges recorded between resource keys.
func (p *Program) ResourceOrder() ([]Key, error) {
	g := graph.New[Key]()
	byString := map[string]Key{}
	for _, r := range p.Resources() {
		g.Add(r.Key().String(), r.Key())
		byString[r.Key().String()] = r.Key()
	}
	for _, e := range p.Edges() {
		if e.Kind == EdgeResolvesTo {
			continue
		}
		if g.Has(e.From.String()) && g.Has(e.To.String()) {
			// `from uses to` means `to` must exist first.
			g.Connect(e.To.String(), e.From.String())
		}
	}
	order, err := g.TopoSort()
	if err != nil {
		return nil, fmt.Errorf("resource dependency graph: %w", err)
	}
	out := make([]Key, 0, len(order))
	for _, k := range order {
		out = append(out, byString[k])
	}
	return out, nil
}
