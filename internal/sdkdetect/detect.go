package sdkdetect

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/cloudcompiler/cloudcc/internal/diag"
	"github.com/cloudcompiler/cloudcc/internal/source"
	ts "github.com/tree-sitter/go-tree-sitter"
)

// Imports records how a file refers to the SDK.
type Imports struct {
	// Modules are the local names bound to the package itself, so that
	// `cloudcc.persist_kv(...)` resolves. `import cloudcompiler as cloudcc` adds "cloudcc".
	Modules map[string]bool
	// Direct maps a local name to the SDK function it refers to.
	// `from cloudcompiler import persist_kv as pkv` adds pkv -> persist_kv.
	Direct map[string]string
	// Alias is the preferred module alias for rewriting, "" when the SDK is
	// only imported through direct names.
	Alias string
}

// Any reports whether the file imports the SDK at all.
func (i Imports) Any() bool { return len(i.Modules) > 0 || len(i.Direct) > 0 }

var callQuery = source.MustQuery(`(call) @call`)

// ResolveImports finds the local names that refer to the SDK in f.
func ResolveImports(f *source.File) Imports {
	imp := Imports{Modules: map[string]bool{}, Direct: map[string]string{}}
	root := f.Root()
	if root == nil {
		return imp
	}
	var walk func(n *ts.Node)
	walk = func(n *ts.Node) {
		switch n.Kind() {
		case "import_statement":
			// import cloudcompiler [as cloudcc][, other]
			for i := uint(0); i < n.NamedChildCount(); i++ {
				child := n.NamedChild(i)
				switch child.Kind() {
				case "dotted_name":
					if f.Text(child) == PackageName {
						imp.Modules[PackageName] = true
						if imp.Alias == "" {
							imp.Alias = PackageName
						}
					}
				case "aliased_import":
					name := child.ChildByFieldName("name")
					alias := child.ChildByFieldName("alias")
					if name != nil && alias != nil && f.Text(name) == PackageName {
						local := f.Text(alias)
						imp.Modules[local] = true
						if imp.Alias == "" {
							imp.Alias = local
						}
					}
				}
			}
		case "import_from_statement":
			mod := n.ChildByFieldName("module_name")
			if mod == nil || f.Text(mod) != PackageName {
				break
			}
			for i := uint(0); i < n.NamedChildCount(); i++ {
				child := n.NamedChild(i)
				if child.Equals(*mod) {
					continue
				}
				switch child.Kind() {
				case "dotted_name":
					name := f.Text(child)
					if _, ok := signatures[name]; ok {
						imp.Direct[name] = name
					}
				case "aliased_import":
					name := child.ChildByFieldName("name")
					alias := child.ChildByFieldName("alias")
					if name == nil || alias == nil {
						continue
					}
					fn := f.Text(name)
					if _, ok := signatures[fn]; ok {
						imp.Direct[f.Text(alias)] = fn
					}
				}
			}
		}
		for i := uint(0); i < n.NamedChildCount(); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(root)
	return imp
}

// Detect finds every SDK hint call in f, appending diagnostics for calls whose
// arguments are not statically evaluable.
func Detect(f *source.File, diags *diag.Diagnostics) []Hint {
	imp := ResolveImports(f)
	if !imp.Any() {
		return nil
	}

	var hints []Hint
	f.Query(callQuery, func(caps map[string]*ts.Node) {
		call := caps["call"]
		fn := call.ChildByFieldName("function")
		if fn == nil {
			return
		}
		name, ok := resolveCallee(f, fn, imp)
		if !ok {
			return
		}
		h, err := buildHint(f, call, name)
		if err != nil {
			line, col := f.PositionAt(int(err.offset))
			diags.Errorf(diag.Position{File: f.Path, Line: line, Col: col},
				signatures[name].Capability, "%s", err.msg)
			return
		}
		hints = append(hints, h)
	})

	sort.SliceStable(hints, func(i, j int) bool { return hints[i].Span[0] < hints[j].Span[0] })
	return hints
}

// resolveCallee maps a call's function expression to an SDK function name.
func resolveCallee(f *source.File, fn *ts.Node, imp Imports) (string, bool) {
	switch fn.Kind() {
	case "identifier":
		name, ok := imp.Direct[f.Text(fn)]
		return name, ok
	case "attribute":
		obj := fn.ChildByFieldName("object")
		attr := fn.ChildByFieldName("attribute")
		if obj == nil || attr == nil || obj.Kind() != "identifier" {
			return "", false
		}
		if !imp.Modules[f.Text(obj)] {
			return "", false
		}
		name := f.Text(attr)
		if _, ok := signatures[name]; !ok {
			return "", false
		}
		return name, true
	}
	return "", false
}

// hintError carries a message plus the byte offset it should be reported at.
type hintError struct {
	msg    string
	offset uint
}

func buildHint(f *source.File, call *ts.Node, fn string) (Hint, *hintError) {
	sig := signatures[fn]
	h := Hint{
		Func:       fn,
		Capability: sig.Capability,
		Args:       map[string]any{},
		File:       f.Path,
		Span:       [2]int{int(call.StartByte()), int(call.EndByte())},
		Receives:   assignedName(f, call),
		Enclosing:  enclosingFunction(f, call),
	}

	args := call.ChildByFieldName("arguments")
	if args == nil {
		return h, &hintError{"malformed call", call.StartByte()}
	}

	positional := 0
	for i := uint(0); i < args.NamedChildCount(); i++ {
		arg := args.NamedChild(i)
		// tree-sitter counts a comment as a named child, so a call with
		// comments between its arguments would otherwise be read as having a
		// comment for an argument. People comment their arguments all the
		// time; this is not an exotic shape.
		if arg.Kind() == "comment" {
			continue
		}
		var p param
		var value *ts.Node

		if arg.Kind() == "keyword_argument" {
			nameNode := arg.ChildByFieldName("name")
			value = arg.ChildByFieldName("value")
			if nameNode == nil || value == nil {
				return h, &hintError{"malformed keyword argument", arg.StartByte()}
			}
			key := f.Text(nameNode)
			found := false
			for _, cand := range sig.Params {
				if cand.Name == key {
					p, found = cand, true
					break
				}
			}
			if !found {
				return h, &hintError{
					msgf("%s() has no parameter %q; expected one of %s", fn, key, strings.Join(ParamNames(fn), ", ")),
					arg.StartByte(),
				}
			}
		} else {
			if positional >= len(sig.Params) {
				return h, &hintError{
					msgf("%s() takes at most %d arguments", fn, len(sig.Params)),
					arg.StartByte(),
				}
			}
			p = sig.Params[positional]
			positional++
			value = arg
		}

		v, err := evalArg(f, value, p)
		if err != nil {
			return h, err
		}
		if v != nil {
			h.Args[p.Name] = v
		}
	}

	for _, p := range sig.Params {
		if p.Required {
			if _, ok := h.Args[p.Name]; !ok {
				return h, &hintError{msgf("%s() requires the %s argument", fn, p.Name), call.StartByte()}
			}
		}
	}
	return h, nil
}

func msgf(format string, args ...any) string {
	return strings.TrimSpace(fmt.Sprintf(format, args...))
}

// evalArg statically evaluates one argument. A nil value with a nil error
// means "explicitly None", which is treated as absent.
func evalArg(f *source.File, n *ts.Node, p param) (any, *hintError) {
	switch p.Kind {
	case pExpr:
		return f.Text(n), nil
	case pString:
		s, ok := stringLiteral(f, n)
		if !ok {
			if n.Kind() == "none" {
				return nil, nil
			}
			return nil, &hintError{
				msgf("%s must be a string literal, not %s (SDK arguments are read at compile time and never executed)", p.Name, describe(f, n)),
				n.StartByte(),
			}
		}
		return s, nil
	case pBool:
		switch n.Kind() {
		case "true":
			return true, nil
		case "false":
			return false, nil
		case "none":
			return nil, nil
		}
		return nil, &hintError{
			msgf("%s must be True or False, not %s", p.Name, describe(f, n)),
			n.StartByte(),
		}
	case pStringList:
		if n.Kind() == "none" {
			return nil, nil
		}
		if n.Kind() != "list" {
			return nil, &hintError{
				msgf("%s must be a list of string literals, not %s", p.Name, describe(f, n)),
				n.StartByte(),
			}
		}
		var out []string
		for i := uint(0); i < n.NamedChildCount(); i++ {
			item := n.NamedChild(i)
			if item.Kind() == "comment" {
				continue
			}
			s, ok := stringLiteral(f, item)
			if !ok {
				// A list of ORM model classes is a common and legitimate
				// shape, so accept bare identifiers as names too.
				if item.Kind() == "identifier" || item.Kind() == "attribute" {
					out = append(out, f.Text(item))
					continue
				}
				return nil, &hintError{
					msgf("%s entries must be string literals or names, not %s", p.Name, describe(f, item)),
					item.StartByte(),
				}
			}
			out = append(out, s)
		}
		return out, nil
	}
	return nil, nil
}

// describe names an argument node for a diagnostic.
func describe(f *source.File, n *ts.Node) string {
	switch n.Kind() {
	case "identifier":
		return "the variable " + f.Text(n)
	case "call":
		return "a function call"
	case "binary_operator":
		return "an expression"
	case "string":
		if hasInterpolation(n) {
			return "an f-string"
		}
		return "a string"
	case "integer", "float":
		return "a number"
	case "attribute":
		return "an attribute lookup"
	case "conditional_expression":
		return "a conditional expression"
	case "parenthesized_expression":
		return "a parenthesized expression"
	}
	return n.Kind()
}

func hasInterpolation(n *ts.Node) bool {
	for i := uint(0); i < n.NamedChildCount(); i++ {
		if n.NamedChild(i).Kind() == "interpolation" {
			return true
		}
	}
	return false
}

// StringLiteral decodes a Python string literal, returning false when the
// value is not knowable without running the program.
//
// Exported because route decorators need exactly the same decoding as hint
// arguments. A second implementation drifted from this one and silently missed
// routes written as concatenated strings, which is the kind of divergence two
// copies of a parser always produce eventually.
func StringLiteral(f *source.File, n *ts.Node) (string, bool) {
	return stringLiteral(f, n)
}

// stringLiteral decodes a Python string literal. f-strings and any string
// containing interpolation are rejected, since their value is not known until
// runtime.
func stringLiteral(f *source.File, n *ts.Node) (string, bool) {
	switch n.Kind() {
	case "string":
		if hasInterpolation(n) {
			return "", false
		}
		raw := f.Text(n)
		if strings.ContainsAny(prefixOf(raw), "fF") {
			return "", false
		}
		var sb strings.Builder
		for i := uint(0); i < n.NamedChildCount(); i++ {
			c := n.NamedChild(i)
			switch c.Kind() {
			case "string_content":
				sb.WriteString(unescape(f.Text(c)))
			case "escape_sequence":
				sb.WriteString(unescape(f.Text(c)))
			}
		}
		return sb.String(), true
	case "concatenated_string":
		var sb strings.Builder
		for i := uint(0); i < n.NamedChildCount(); i++ {
			child := n.NamedChild(i)
			if child.Kind() == "comment" {
				continue
			}
			part, ok := stringLiteral(f, child)
			if !ok {
				return "", false
			}
			sb.WriteString(part)
		}
		return sb.String(), true
	case "parenthesized_expression":
		// Wrapping a long string in parentheses and splitting it across lines
		// is what Black does to keep within the line limit, so this shape
		// turns up in ordinary formatted code. Rejecting it would mean running
		// a formatter could break a program that compiled a moment earlier.
		var inner *ts.Node
		for i := uint(0); i < n.NamedChildCount(); i++ {
			if child := n.NamedChild(i); child.Kind() != "comment" {
				if inner != nil {
					return "", false
				}
				inner = child
			}
		}
		if inner != nil {
			return stringLiteral(f, inner)
		}
	}
	return "", false
}

// prefixOf returns the literal prefix characters before the opening quote.
func prefixOf(raw string) string {
	for i, r := range raw {
		if r == '\'' || r == '"' {
			return raw[:i]
		}
	}
	return ""
}

// unescape resolves the escape sequences that can appear in an identifier or
// path literal. Unknown sequences are left as written.
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	if out, err := strconv.Unquote(`"` + strings.ReplaceAll(s, `"`, `\"`) + `"`); err == nil {
		return out
	}
	return s
}

// assignedName returns the identifier a call's result is bound to, which is
// what later passes match method calls (topic.publish, pets.get) against.
func assignedName(f *source.File, call *ts.Node) string {
	parent := call.Parent()
	if parent == nil || parent.Kind() != "assignment" {
		return ""
	}
	left := parent.ChildByFieldName("left")
	if left == nil || left.Kind() != "identifier" {
		return ""
	}
	return f.Text(left)
}

// enclosingFunction returns the name of the innermost function definition
// containing the call, or "" when the call is at module level.
func enclosingFunction(f *source.File, call *ts.Node) string {
	for n := call.Parent(); n != nil; n = n.Parent() {
		if n.Kind() == "function_definition" {
			if name := n.ChildByFieldName("name"); name != nil {
				return f.Text(name)
			}
			return "?"
		}
	}
	return ""
}
