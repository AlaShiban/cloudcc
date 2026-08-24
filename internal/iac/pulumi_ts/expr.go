package pulumi_ts

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/cloudcompiler/cloudcc/internal/ir"
	"github.com/cloudcompiler/cloudcc/internal/sanitize"
)

// varNamer assigns each resource its generated TypeScript variable name.
type varNamer struct {
	names map[string]string
	taken map[string]bool
}

func newVarNamer() *varNamer {
	return &varNamer{names: map[string]string{}, taken: map[string]bool{}}
}

// assign gives a resource a stable, unique variable name. Names come from the
// resource id plus the template's suffix, so the Lambda and the IAM role for
// unit "api" become apiFn and apiRole rather than colliding.
func (v *varNamer) assign(key ir.Key, suffix string) string {
	if existing, ok := v.names[key.String()]; ok {
		return existing
	}
	base := sanitize.Identifier(key.ID + "-" + suffix)
	name := base
	for i := 2; v.taken[name]; i++ {
		name = fmt.Sprintf("%s%d", base, i)
	}
	v.taken[name] = true
	v.names[key.String()] = name
	return name
}

func (v *varNamer) get(key ir.Key) (string, bool) {
	name, ok := v.names[key.String()]
	return name, ok
}

// reserve records a name that is not a resource, so no resource can take it.
func (v *varNamer) reserve(name string) { v.taken[name] = true }

// value renders a resolved property as a TypeScript expression.
func (v *varNamer) value(x any) (string, error) {
	switch typed := x.(type) {
	case nil:
		return "undefined", nil
	case ir.Raw:
		return string(typed), nil
	case ir.Ref:
		name, ok := v.get(typed.Key)
		if !ok {
			return "", fmt.Errorf("reference to %s, which no resource defines", typed.Key)
		}
		return name + "." + typed.Prop, nil
	case ir.Interp:
		return v.interp(typed)
	case ir.JSONDoc:
		inner, err := v.value(typed.Value)
		if err != nil {
			return "", err
		}
		// jsonStringify resolves any Outputs inside the document before
		// serialising, which is exactly what an IAM policy full of ARNs needs.
		return "pulumi.jsonStringify(" + inner + ")", nil
	case ir.EnvBinding:
		return "", fmt.Errorf("an EnvBinding cannot be rendered as a property value")
	case string:
		return quote(typed), nil
	case bool:
		if typed {
			return "true", nil
		}
		return "false", nil
	case int:
		return fmt.Sprintf("%d", typed), nil
	case int64:
		return fmt.Sprintf("%d", typed), nil
	case float64:
		return trimFloat(typed), nil
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			rendered, err := v.value(item)
			if err != nil {
				return "", err
			}
			parts = append(parts, rendered)
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	case []string:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			parts = append(parts, quote(item))
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	case map[string]any:
		return v.object(typed, 0)
	case map[string]string:
		converted := make(map[string]any, len(typed))
		for k, item := range typed {
			converted[k] = item
		}
		return v.object(converted, 0)
	}
	return "", fmt.Errorf("cannot render %T as a TypeScript value", x)
}

// object renders a map as an object literal with sorted keys, so the emitted
// project is byte-identical across runs (D18).
func (v *varNamer) object(m map[string]any, depth int) (string, error) {
	if len(m) == 0 {
		return "{}", nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	indent := strings.Repeat("    ", depth+1)
	closing := strings.Repeat("    ", depth)

	var b strings.Builder
	b.WriteString("{\n")
	for _, k := range keys {
		rendered, err := v.valueAtDepth(m[k], depth+1)
		if err != nil {
			return "", fmt.Errorf("%s: %w", k, err)
		}
		fmt.Fprintf(&b, "%s%s: %s,\n", indent, propertyKey(k), rendered)
	}
	b.WriteString(closing)
	b.WriteString("}")
	return b.String(), nil
}

func (v *varNamer) valueAtDepth(x any, depth int) (string, error) {
	switch typed := x.(type) {
	case map[string]any:
		return v.object(typed, depth)
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			rendered, err := v.valueAtDepth(item, depth+1)
			if err != nil {
				return "", err
			}
			parts = append(parts, rendered)
		}
		if len(parts) == 0 {
			return "[]", nil
		}
		indent := strings.Repeat("    ", depth+1)
		closing := strings.Repeat("    ", depth)
		return "[\n" + indent + strings.Join(parts, ",\n"+indent) + ",\n" + closing + "]", nil
	}
	return v.value(x)
}

// interp renders a template literal, using pulumi.interpolate when any part is
// a resource output so the string resolves at deploy time.
func (v *varNamer) interp(in ir.Interp) (string, error) {
	var b strings.Builder
	needsOutput := false
	for _, part := range in.Parts {
		switch typed := part.(type) {
		case string:
			b.WriteString(escapeTemplate(typed))
		case ir.Ref:
			name, ok := v.get(typed.Key)
			if !ok {
				return "", fmt.Errorf("reference to %s, which no resource defines", typed.Key)
			}
			needsOutput = true
			b.WriteString("${" + name + "." + typed.Prop + "}")
		case ir.Raw:
			needsOutput = true
			b.WriteString("${" + string(typed) + "}")
		default:
			rendered, err := v.value(part)
			if err != nil {
				return "", err
			}
			b.WriteString("${" + rendered + "}")
		}
	}
	prefix := ""
	if needsOutput {
		prefix = "pulumi.interpolate"
	}
	return prefix + "`" + b.String() + "`", nil
}

// propertyKey quotes an object key only when it is not a plain identifier.
func propertyKey(k string) string {
	if k == "" {
		return `""`
	}
	for i, r := range k {
		ok := r == '_' || r == '$' ||
			r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' ||
			(i > 0 && r >= '0' && r <= '9')
		if !ok {
			return quote(k)
		}
	}
	return k
}

func quote(s string) string {
	// encoding/json produces a correctly escaped double-quoted string, which
	// is a valid TypeScript string literal.
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

func escapeTemplate(s string) string {
	return strings.NewReplacer("\\", `\\`, "`", "\\`", "${", "\\${").Replace(s)
}

func trimFloat(f float64) string {
	if f == float64(int64(f)) {
		return fmt.Sprintf("%d", int64(f))
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%f", f), "0"), ".")
}
