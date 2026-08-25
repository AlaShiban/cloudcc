package python

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/cloudcompiler/cloudcc/internal/diag"
	"github.com/cloudcompiler/cloudcc/internal/sdkdetect"
	"github.com/cloudcompiler/cloudcc/internal/source"
	ts "github.com/tree-sitter/go-tree-sitter"
)

// imports records how a file refers to the SDK.
type imports struct {
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
func (i imports) Any() bool { return len(i.Modules) > 0 || len(i.Direct) > 0 }

var callQuery = source.MustQuery(source.PythonLanguage(), `(call) @call`)

// ResolveImports finds the local names that refer to the SDK in f.
func resolveImports(f *source.File) imports {
	imp := imports{Modules: map[string]bool{}, Direct: map[string]string{}}
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
					if f.Text(child) == sdkdetect.PackageName {
						imp.Modules[sdkdetect.PackageName] = true
						if imp.Alias == "" {
							imp.Alias = sdkdetect.PackageName
						}
					}
				case "aliased_import":
					name := child.ChildByFieldName("name")
					alias := child.ChildByFieldName("alias")
					if name != nil && alias != nil && f.Text(name) == sdkdetect.PackageName {
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
			if mod == nil || f.Text(mod) != sdkdetect.PackageName {
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
					if _, ok := sdkdetect.Lookup(name); ok {
						imp.Direct[name] = name
					}
				case "aliased_import":
					name := child.ChildByFieldName("name")
					alias := child.ChildByFieldName("alias")
					if name == nil || alias == nil {
						continue
					}
					fn := f.Text(name)
					if _, ok := sdkdetect.Lookup(fn); ok {
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
func detectHints(f *source.File, diags *diag.Diagnostics) []sdkdetect.Hint {
	imp := resolveImports(f)
	if !imp.Any() {
		return nil
	}

	var hints []sdkdetect.Hint
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
				capabilityOf(name), "%s", err.msg)
			return
		}
		hints = append(hints, h)
	})

	sort.SliceStable(hints, func(i, j int) bool { return hints[i].Span[0] < hints[j].Span[0] })
	return hints
}

// resolveCallee maps a call's function expression to an SDK function name.
func resolveCallee(f *source.File, fn *ts.Node, imp imports) (string, bool) {
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
		if _, ok := sdkdetect.Lookup(name); !ok {
			return "", false
		}
		return name, true
	}
	return "", false
}

// capabilityOf is the config kind an SDK function contributes to.
func capabilityOf(fn string) string {
	sig, _ := sdkdetect.Lookup(fn)
	return sig.Capability
}

// hintError carries a message plus the byte offset it should be reported at.
type hintError struct {
	msg    string
	offset uint
}

func buildHint(f *source.File, call *ts.Node, fn string) (sdkdetect.Hint, *hintError) {
	sig, _ := sdkdetect.Lookup(fn)
	h := sdkdetect.Hint{
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
		var p sdkdetect.Param
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
					msgf("%s() has no parameter %q; expected one of %s", fn, key, strings.Join(sdkdetect.ParamNames(fn), ", ")),
					arg.StartByte(),
				}
			}
		} else {
			allowed := sdkdetect.PositionalParams(fn)
			if positional >= len(allowed) {
				return h, &hintError{
					msgf("%s() takes at most %d positional argument(s)", fn, len(allowed)),
					arg.StartByte(),
				}
			}
			p = allowed[positional]
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

	if sig.Capability == "" {
		if err := resolveClient(f, call, &h); err != nil {
			return h, err
		}
	}
	return h, nil
}

// resolveClient reads the capability from the type of the client being
// wrapped. This is what makes one verb enough: persist(Redis()) and
// persist(create_engine(...)) are the same call, and the argument decides
// whether a cache or a database gets provisioned.
func resolveClient(f *source.File, call *ts.Node, h *sdkdetect.Hint) *hintError {
	args := call.ChildByFieldName("arguments")
	clientNode := positionalArg(f, args, 0)
	if clientNode == nil {
		return &hintError{"persist() requires a client to wrap", call.StartByte()}
	}
	h.Client = f.Text(clientNode)

	constructor, ctorCall := constructorOf(f, clientNode)
	if constructor == "" {
		return &hintError{
			msgf("persist() needs a client whose type says what to provision, not %s; "+
				"pass one of %s", describe(f, clientNode),
				strings.Join(sdkdetect.KnownClients("python"), ", ")),
			clientNode.StartByte(),
		}
	}

	client, ok := sdkdetect.LookupClient("python", constructor)
	if !ok {
		return &hintError{
			msgf("persist() does not recognise %s(); it understands %s",
				constructor, strings.Join(sdkdetect.KnownClients("python"), ", ")),
			clientNode.StartByte(),
		}
	}

	h.Capability = client.Capability
	h.ClientType = client.Type
	h.ClientLibrary = client.Library

	// The SDK's own clients take declarations rather than connection settings,
	// so their arguments are the compiler's business. A library's are not:
	// Redis(host=...) is talking to the local Redis, and where that is has
	// nothing to do with what gets provisioned.
	if client.Library == "" {
		args, err := clientKeywordArgs(f, ctorCall)
		if err != nil {
			return err
		}
		h.ClientArgs = args
	}
	if client.Type == "" {
		// A library that speaks to several engines still supplies the default,
		// read from the connection URL it was given.
		h.ClientType = sdkdetect.RelationalType(firstStringArg(f, ctorCall))
	}
	return nil
}

// clientKeywordArgs reads the literal keyword arguments of an SDK client
// constructor.
//
// Every one of them is a declaration the compiler acts on, so a non-literal is
// an error for the same reason `id=name` is: reading it would mean running the
// program.
func clientKeywordArgs(f *source.File, call *ts.Node) (map[string]any, *hintError) {
	if call == nil {
		return nil, nil
	}
	args := call.ChildByFieldName("arguments")
	if args == nil {
		return nil, nil
	}
	out := map[string]any{}
	for i := uint(0); i < args.NamedChildCount(); i++ {
		arg := args.NamedChild(i)
		if arg.Kind() != "keyword_argument" {
			continue
		}
		name := f.Text(arg.ChildByFieldName("name"))
		value := arg.ChildByFieldName("value")
		if value == nil {
			continue
		}
		literal, err := clientLiteral(f, name, value)
		if err != nil {
			return nil, err
		}
		out[name] = literal
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// clientLiteral evaluates one such argument. Strings, booleans and integers
// are the whole vocabulary: these are declarations, not expressions.
func clientLiteral(f *source.File, name string, n *ts.Node) (any, *hintError) {
	if s, ok := stringLiteral(f, n); ok {
		return s, nil
	}
	switch n.Kind() {
	case "true":
		return true, nil
	case "false":
		return false, nil
	case "integer":
		v, err := strconv.Atoi(strings.TrimSpace(f.Text(n)))
		if err == nil {
			return v, nil
		}
	}
	return nil, &hintError{
		msgf("%s must be a literal, not %s (SDK arguments are read at compile "+
			"time and never executed)", name, describe(f, n)),
		n.StartByte(),
	}
}

// positionalArg returns the nth positional argument of a call.
func positionalArg(f *source.File, args *ts.Node, n int) *ts.Node {
	if args == nil {
		return nil
	}
	seen := 0
	for i := uint(0); i < args.NamedChildCount(); i++ {
		arg := args.NamedChild(i)
		if arg.Kind() == "comment" || arg.Kind() == "keyword_argument" {
			continue
		}
		if seen == n {
			return arg
		}
		seen++
	}
	return nil
}

// constructorOf returns the name at the head of a client expression, and the
// call it belongs to. `redis.Redis(host=...)` and `Redis()` both give "Redis".
func constructorOf(f *source.File, n *ts.Node) (string, *ts.Node) {
	if n.Kind() != "call" {
		return "", nil
	}
	fn := n.ChildByFieldName("function")
	if fn == nil {
		return "", nil
	}
	switch fn.Kind() {
	case "identifier":
		return f.Text(fn), n
	case "attribute":
		if attr := fn.ChildByFieldName("attribute"); attr != nil {
			return f.Text(attr), n
		}
	}
	return "", nil
}

// firstStringArg reads the first string literal a call was given, which for a
// database client is its connection URL.
func firstStringArg(f *source.File, call *ts.Node) string {
	if call == nil {
		return ""
	}
	args := call.ChildByFieldName("arguments")
	if args == nil {
		return ""
	}
	for i := uint(0); i < args.NamedChildCount(); i++ {
		arg := args.NamedChild(i)
		if arg.Kind() == "comment" {
			continue
		}
		if arg.Kind() == "keyword_argument" {
			arg = arg.ChildByFieldName("value")
			if arg == nil {
				continue
			}
		}
		if s, ok := stringLiteral(f, arg); ok {
			return s
		}
	}
	return ""
}

func msgf(format string, args ...any) string {
	return strings.TrimSpace(fmt.Sprintf(format, args...))
}

// evalArg statically evaluates one argument. A nil value with a nil error
// means "explicitly None", which is treated as absent.
func evalArg(f *source.File, n *ts.Node, p sdkdetect.Param) (any, *hintError) {
	switch p.Kind {
	case sdkdetect.ParamExpr:
		return f.Text(n), nil

	case sdkdetect.ParamClient:
		// The client's type is the declaration. What matters is the
		// constructor at the head of the expression, not the whole thing.
		return f.Text(n), nil
	case sdkdetect.ParamString:
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
	case sdkdetect.ParamBool:
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
	case sdkdetect.ParamStringList:
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
