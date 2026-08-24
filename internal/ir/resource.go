package ir

// GenericResource is the single Resource implementation. Because resolution is
// data-driven (D11), a concrete resource is fully described by its key,
// template name, props and env outputs -- there is nothing per-type to
// subclass.
type GenericResource struct {
	K    Key               `json:"key"`
	Tmpl string            `json:"template"`
	P    map[string]any    `json:"props"`
	Env  map[string]string `json:"env_outputs,omitempty"`
}

// NewResource builds a resource node.
func NewResource(kind, id, template string, props map[string]any, env map[string]string) *GenericResource {
	if props == nil {
		props = map[string]any{}
	}
	return &GenericResource{K: Key{Kind: kind, ID: id}, Tmpl: template, P: props, Env: env}
}

func (r *GenericResource) Key() Key                      { return r.K }
func (r *GenericResource) Template() string              { return r.Tmpl }
func (r *GenericResource) Props() map[string]any         { return r.P }
func (r *GenericResource) EnvOutputs() map[string]string { return r.Env }

var _ Resource = (*GenericResource)(nil)
