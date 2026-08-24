package ir

import "encoding/json"

// The provider resolver produces resource properties as plain Go values, but
// some of those values have to reference another resource's output, which only
// exists at deploy time. These four types are the whole vocabulary for that.
// The IaC backend knows how to render each one; the resolver never writes a
// line of TypeScript.

// Ref is another resource's output property, e.g. the ARN of a table.
type Ref struct {
	Key  Key    `json:"$ref"`
	Prop string `json:"prop"`
}

// Raw is backend-specific source emitted verbatim. Use it sparingly: every
// occurrence is a place where the output stops being backend-neutral.
type Raw string

// MarshalJSON renders Raw as a tagged object so --dump-ir stays honest about
// what is a literal and what is an escape hatch.
func (r Raw) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{"$raw": string(r)})
}

// Interp is a string built from literal parts and resource references, the
// equivalent of a template literal.
type Interp struct {
	Parts []any `json:"$interp"`
}

// JSONDoc is a value that must be serialised to a JSON string at deploy time,
// after any references it contains have resolved. IAM policy documents are the
// reason it exists.
type JSONDoc struct {
	Value any `json:"$json"`
}

// Lit builds an Interp from alternating literal and reference parts.
func Lit(parts ...any) Interp { return Interp{Parts: parts} }

// ContainsRef reports whether v holds a Ref anywhere inside it. The IaC
// backend uses this to decide whether a property needs to be resolved through
// an apply.
func ContainsRef(v any) bool {
	switch typed := v.(type) {
	case Ref:
		return true
	case Interp:
		for _, p := range typed.Parts {
			if ContainsRef(p) {
				return true
			}
		}
	case JSONDoc:
		return ContainsRef(typed.Value)
	case map[string]any:
		for _, item := range typed {
			if ContainsRef(item) {
				return true
			}
		}
	case []any:
		for _, item := range typed {
			if ContainsRef(item) {
				return true
			}
		}
	}
	return false
}

// RefsIn returns every Ref inside v, in a stable order.
func RefsIn(v any) []Ref {
	var out []Ref
	var walk func(any)
	walk = func(v any) {
		switch typed := v.(type) {
		case Ref:
			out = append(out, typed)
		case Interp:
			for _, p := range typed.Parts {
				walk(p)
			}
		case JSONDoc:
			walk(typed.Value)
		case map[string]any:
			for _, k := range sortedMapKeys(typed) {
				walk(typed[k])
			}
		case []any:
			for _, item := range typed {
				walk(item)
			}
		}
	}
	walk(v)
	return out
}

func sortedMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// sort.Strings without importing sort in the hot path of a tiny map.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
