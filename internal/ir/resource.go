package ir

// EnvBinding is one environment variable an execution unit receives from a
// resource it uses. Exactly one field is set: Prop names a property of the
// resource itself, Expr is any expression the IaC backend can render.
type EnvBinding struct {
	Prop string `json:"prop,omitempty"`
	Expr any    `json:"expr,omitempty"`
}

// FromProp binds an environment variable to a property of the resource.
func FromProp(prop string) EnvBinding { return EnvBinding{Prop: prop} }

// FromExpr binds an environment variable to an arbitrary expression.
func FromExpr(expr any) EnvBinding { return EnvBinding{Expr: expr} }

// Env is a convenience constructor for a resource's environment bindings.
func Env(pairs ...any) map[string]EnvBinding {
	out := map[string]EnvBinding{}
	for i := 0; i+1 < len(pairs); i += 2 {
		name, _ := pairs[i].(string)
		switch v := pairs[i+1].(type) {
		case string:
			out[name] = FromProp(v)
		case EnvBinding:
			out[name] = v
		default:
			out[name] = FromExpr(v)
		}
	}
	return out
}

// GenericResource is the single Resource implementation. Because resolution is
// data-driven (D11), a concrete resource is fully described by its key,
// template name, props and env bindings -- there is nothing per-type to
// subclass.
type GenericResource struct {
	K    Key                   `json:"key"`
	Tmpl string                `json:"template"`
	P    map[string]any        `json:"props"`
	E    map[string]EnvBinding `json:"env_outputs,omitempty"`
}

// NewResource builds a resource node.
func NewResource(kind, id, template string, props map[string]any, env map[string]EnvBinding) *GenericResource {
	if props == nil {
		props = map[string]any{}
	}
	return &GenericResource{K: Key{Kind: kind, ID: id}, Tmpl: template, P: props, E: env}
}

func (r *GenericResource) Key() Key                          { return r.K }
func (r *GenericResource) Template() string                  { return r.Tmpl }
func (r *GenericResource) Props() map[string]any             { return r.P }
func (r *GenericResource) EnvOutputs() map[string]EnvBinding { return r.E }

var _ Resource = (*GenericResource)(nil)
