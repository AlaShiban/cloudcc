// Package node is the JavaScript and TypeScript frontend.
//
// The two languages share every syntactic shape this compiler cares about --
// imports, calls, object literals, string literals -- so one set of queries
// covers both, with only the grammar differing by extension.
package node

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

// Package is the importable name of the SDK.
const Package = "@cloudcompiler/sdk"

// sdkFunction maps the SDK's JavaScript spelling to its canonical name. The
// capability surface is one thing across languages; only the spelling differs.
var sdkFunction = map[string]string{
	"persist":       sdkdetect.FnPersist,
	"executionUnit": sdkdetect.FnExecutionUnit,
	"expose":        sdkdetect.FnExpose,
	"configValue":   sdkdetect.FnConfigValue,
	"staticUnit":    sdkdetect.FnStaticUnit,
	"embedAssets":   sdkdetect.FnEmbedAssets,
}

// jsName is the inverse, for the rewriter and for diagnostics.
var jsName = func() map[string]string {
	out := map[string]string{}
	for js, canonical := range sdkFunction {
		out[canonical] = js
	}
	return out
}()

// optionProperty maps a JavaScript option property to its SDK parameter.
// JavaScript spells options in camelCase where Python spells them with
// underscores; everything else passes through unchanged.
var optionProperty = map[string]string{
	"staticFiles":   "static_files",
	"indexDocument": "index_document",
	"sharedFiles":   "shared_files",
}

func paramFor(property string) string {
	if mapped, ok := optionProperty[property]; ok {
		return mapped
	}
	return property
}

// imports records how a module refers to the SDK.
type imports struct {
	// direct maps a local binding to the SDK function it refers to, covering
	// `import { persistKv }`, `import { persistKv as kv }` and the destructured
	// `const { persistKv } = require(...)` form.
	direct map[string]string
	// namespaces are bindings for the whole module, so `sdk.persistKv(...)`
	// resolves. Covers `import * as sdk`, a default import, and
	// `const sdk = require(...)`.
	namespaces map[string]bool
	// spans are the byte ranges of the statements that import the SDK, which
	// the rewriter removes: the SDK is not installed in a deployment bundle.
	spans [][2]int
}

func (i imports) any() bool { return len(i.direct) > 0 || len(i.namespaces) > 0 }

// resolveImports finds the local names that refer to the SDK.
func resolveImports(f *source.File) imports {
	imp := imports{direct: map[string]string{}, namespaces: map[string]bool{}}
	root := f.Root()
	if root == nil {
		return imp
	}

	var walk func(n *ts.Node)
	walk = func(n *ts.Node) {
		switch n.Kind() {
		case "import_statement":
			if src := n.ChildByFieldName("source"); src == nil || literalString(f, src) != Package {
				break
			}
			imp.spans = append(imp.spans, statementSpan(f, n))
			for i := uint(0); i < n.NamedChildCount(); i++ {
				collectClause(f, n.NamedChild(i), &imp)
			}
			return

		case "lexical_declaration", "variable_declaration":
			// const { persistKv } = require("@cloudcompiler/sdk")
			// const sdk = require("@cloudcompiler/sdk")
			if !declarationRequires(f, n) {
				break
			}
			imp.spans = append(imp.spans, statementSpan(f, n))
			for i := uint(0); i < n.NamedChildCount(); i++ {
				d := n.NamedChild(i)
				if d.Kind() != "variable_declarator" {
					continue
				}
				collectBinding(f, d.ChildByFieldName("name"), &imp)
			}
			return
		}
		for i := uint(0); i < n.NamedChildCount(); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(root)
	return imp
}

// collectClause reads an ESM import clause.
func collectClause(f *source.File, n *ts.Node, imp *imports) {
	switch n.Kind() {
	case "import_clause":
		for i := uint(0); i < n.NamedChildCount(); i++ {
			collectClause(f, n.NamedChild(i), imp)
		}
	case "named_imports":
		for i := uint(0); i < n.NamedChildCount(); i++ {
			spec := n.NamedChild(i)
			if spec.Kind() != "import_specifier" {
				continue
			}
			name := f.Text(spec.ChildByFieldName("name"))
			local := name
			if alias := spec.ChildByFieldName("alias"); alias != nil {
				local = f.Text(alias)
			}
			if canonical, ok := sdkFunction[name]; ok {
				imp.direct[local] = canonical
			}
		}
	case "namespace_import":
		// import * as sdk from "..."
		for i := uint(0); i < n.NamedChildCount(); i++ {
			if child := n.NamedChild(i); child.Kind() == "identifier" {
				imp.namespaces[f.Text(child)] = true
			}
		}
	case "identifier":
		// A default import. The SDK has no default export, but binding one is
		// harmless and people do write it, so the name is treated as the
		// namespace rather than being silently ignored.
		imp.namespaces[f.Text(n)] = true
	}
}

// collectBinding reads the left-hand side of a require().
func collectBinding(f *source.File, n *ts.Node, imp *imports) {
	if n == nil {
		return
	}
	switch n.Kind() {
	case "identifier":
		imp.namespaces[f.Text(n)] = true
	case "object_pattern":
		for i := uint(0); i < n.NamedChildCount(); i++ {
			el := n.NamedChild(i)
			switch el.Kind() {
			case "shorthand_property_identifier_pattern":
				name := f.Text(el)
				if canonical, ok := sdkFunction[name]; ok {
					imp.direct[name] = canonical
				}
			case "pair_pattern":
				key := f.Text(el.ChildByFieldName("key"))
				value := el.ChildByFieldName("value")
				if canonical, ok := sdkFunction[key]; ok && value != nil {
					imp.direct[f.Text(value)] = canonical
				}
			}
		}
	}
}

// declarationRequires reports whether a declaration's initialiser is a
// require() of the SDK.
func declarationRequires(f *source.File, n *ts.Node) bool {
	for i := uint(0); i < n.NamedChildCount(); i++ {
		d := n.NamedChild(i)
		if d.Kind() != "variable_declarator" {
			continue
		}
		value := d.ChildByFieldName("value")
		if value == nil || value.Kind() != "call_expression" {
			continue
		}
		fn := value.ChildByFieldName("function")
		if fn == nil || f.Text(fn) != "require" {
			continue
		}
		args := value.ChildByFieldName("arguments")
		if args == nil {
			continue
		}
		for j := uint(0); j < args.NamedChildCount(); j++ {
			if literalString(f, args.NamedChild(j)) == Package {
				return true
			}
		}
	}
	return false
}

// statementSpan is a statement's byte range, extended over a trailing
// semicolon and newline so removing it leaves no blank line behind.
func statementSpan(f *source.File, n *ts.Node) [2]int {
	start, end := int(n.StartByte()), int(n.EndByte())
	for end < len(f.Content) && (f.Content[end] == ';' || f.Content[end] == '\r') {
		end++
	}
	if end < len(f.Content) && f.Content[end] == '\n' {
		end++
	}
	return [2]int{start, end}
}

// detectHints finds every SDK hint call in a parsed file.
func detectHints(f *source.File, diags *diag.Diagnostics) []sdkdetect.Hint {
	imp := resolveImports(f)
	if !imp.any() {
		return nil
	}

	var hints []sdkdetect.Hint
	f.Query(queryFor(f), func(caps map[string]*ts.Node) {
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
			line, col := f.PositionAt(err.offset)
			diags.Errorf(diag.Position{File: f.Path, Line: line, Col: col},
				capabilityOf(name), "%s", err.msg)
			return
		}
		hints = append(hints, h)
	})

	sort.SliceStable(hints, func(i, j int) bool { return hints[i].Span[0] < hints[j].Span[0] })
	return hints
}

// resolveCallee maps a call's callee to an SDK function name.
func resolveCallee(f *source.File, fn *ts.Node, imp imports) (string, bool) {
	switch fn.Kind() {
	case "identifier":
		name, ok := imp.direct[f.Text(fn)]
		return name, ok
	case "member_expression":
		object := fn.ChildByFieldName("object")
		property := fn.ChildByFieldName("property")
		if object == nil || property == nil || object.Kind() != "identifier" {
			return "", false
		}
		if !imp.namespaces[f.Text(object)] {
			return "", false
		}
		canonical, ok := sdkFunction[f.Text(property)]
		return canonical, ok
	}
	return "", false
}

type hintError struct {
	msg    string
	offset int
}

// buildHint reads a call's arguments against the SDK signature.
//
// JavaScript spells optional arguments as a trailing options object where
// Python uses keywords, so an object literal argument contributes its
// properties as named arguments and everything else fills the positional
// parameters in order. That one rule covers every shape in the SDK, including
// executionUnit({id}) where the object *is* the first argument.
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
		return h, &hintError{"malformed call", int(call.StartByte())}
	}

	positional := 0
	for i := uint(0); i < args.NamedChildCount(); i++ {
		arg := args.NamedChild(i)
		// tree-sitter counts a comment as a named child, and people comment
		// their arguments.
		if arg.Kind() == "comment" {
			continue
		}

		if arg.Kind() == "object" {
			if err := readOptions(f, arg, sig, &h); err != nil {
				return h, err
			}
			continue
		}

		// Everything the SDK declares in an options object is keyword-only,
		// and readOptions above is what reaches it. Only the rest can appear
		// here, so a call spelled positionally that the SDK would reject is
		// rejected at compile time too.
		allowed := sdkdetect.PositionalParams(fn)
		if positional >= len(allowed) {
			return h, &hintError{
				fmt.Sprintf("%s() takes at most %d positional argument(s)", jsName[fn], len(allowed)),
				int(arg.StartByte()),
			}
		}
		p := allowed[positional]
		positional++
		v, err := evalArg(f, arg, p)
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
				return h, &hintError{
					fmt.Sprintf("%s() requires the %s argument", jsName[fn], propertyName(p.Name)),
					int(call.StartByte()),
				}
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
// wrapped, which is what makes one verb enough.
func resolveClient(f *source.File, call *ts.Node, h *sdkdetect.Hint) *hintError {
	args := call.ChildByFieldName("arguments")
	clientNode := positionalArg(args, 0)
	if clientNode == nil {
		return &hintError{"persist() requires a client to wrap", int(call.StartByte())}
	}
	h.Client = f.Text(clientNode)

	constructor, ctorCall := constructorOf(f, clientNode)
	if constructor == "" {
		return &hintError{
			fmt.Sprintf("persist() needs a client whose type says what to provision, not %s; "+
				"pass one of %s", describe(f, clientNode),
				strings.Join(sdkdetect.KnownClients("node"), ", ")),
			int(clientNode.StartByte()),
		}
	}

	client, ok := sdkdetect.LookupClient("node", constructor)
	if !ok {
		return &hintError{
			fmt.Sprintf("persist() does not recognise %s(); it understands %s",
				constructor, strings.Join(sdkdetect.KnownClients("node"), ", ")),
			int(clientNode.StartByte()),
		}
	}

	h.Capability = client.Capability
	h.ClientType = client.Type
	if client.Type == "" {
		h.ClientType = sdkdetect.RelationalType(firstStringArg(f, ctorCall))
	}
	return nil
}

// positionalArg returns the nth positional argument of a call.
func positionalArg(args *ts.Node, n int) *ts.Node {
	if args == nil {
		return nil
	}
	seen := 0
	for i := uint(0); i < args.NamedChildCount(); i++ {
		arg := args.NamedChild(i)
		if arg.Kind() == "comment" || arg.Kind() == "object" {
			continue
		}
		if seen == n {
			return arg
		}
		seen++
	}
	return nil
}

// constructorOf returns the name at the head of a client expression.
// `new Redis(...)`, `redis.createClient(...)` and `createClient()` all reduce
// to the name that identifies the library.
func constructorOf(f *source.File, n *ts.Node) (string, *ts.Node) {
	switch n.Kind() {
	case "new_expression":
		if ctor := n.ChildByFieldName("constructor"); ctor != nil {
			return trailingName(f, ctor), n
		}
	case "call_expression":
		if fn := n.ChildByFieldName("function"); fn != nil {
			return trailingName(f, fn), n
		}
	}
	return "", nil
}

// trailingName is the last segment of a possibly-qualified name.
func trailingName(f *source.File, n *ts.Node) string {
	switch n.Kind() {
	case "identifier":
		return f.Text(n)
	case "member_expression":
		if prop := n.ChildByFieldName("property"); prop != nil {
			return f.Text(prop)
		}
	}
	return ""
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
		if s, ok := stringLiteral(f, arg); ok {
			return s
		}
		// `new Pool({ connectionString: "postgres://..." })`
		if arg.Kind() == "object" {
			for j := uint(0); j < arg.NamedChildCount(); j++ {
				pair := arg.NamedChild(j)
				if pair.Kind() != "pair" {
					continue
				}
				if s, ok := stringLiteral(f, pair.ChildByFieldName("value")); ok {
					return s
				}
			}
		}
	}
	return ""
}

// readOptions reads a trailing options object.
func readOptions(f *source.File, obj *ts.Node, sig sdkdetect.Signature, h *sdkdetect.Hint) *hintError {
	for i := uint(0); i < obj.NamedChildCount(); i++ {
		el := obj.NamedChild(i)
		if el.Kind() == "comment" {
			continue
		}
		var key string
		var value *ts.Node
		switch el.Kind() {
		case "pair":
			key = propertyKeyName(f, el.ChildByFieldName("key"))
			value = el.ChildByFieldName("value")
		case "shorthand_property_identifier":
			// `{ id }` refers to a variable, which is not knowable statically.
			return &hintError{
				fmt.Sprintf("%s must be written out as a literal; a shorthand property refers to a variable, "+
					"and SDK arguments are read at compile time and never executed", f.Text(el)),
				int(el.StartByte()),
			}
		default:
			continue
		}
		if value == nil {
			continue
		}

		name := paramFor(key)
		var p sdkdetect.Param
		found := false
		for _, cand := range sig.Params {
			if cand.Name == name {
				p, found = cand, true
				break
			}
		}
		if !found {
			return &hintError{
				fmt.Sprintf("%q is not an option of %s(); expected one of %s",
					key, jsName[h.Func], strings.Join(propertyNames(sig), ", ")),
				int(el.StartByte()),
			}
		}
		v, err := evalArg(f, value, p)
		if err != nil {
			return err
		}
		if v != nil {
			h.Args[p.Name] = v
		}
	}
	return nil
}

func propertyKeyName(f *source.File, n *ts.Node) string {
	if n == nil {
		return ""
	}
	if n.Kind() == "string" {
		return literalString(f, n)
	}
	return f.Text(n)
}

// propertyName is the JavaScript spelling of an SDK parameter.
func propertyName(param string) string {
	for js, canonical := range optionProperty {
		if canonical == param {
			return js
		}
	}
	return param
}

func propertyNames(sig sdkdetect.Signature) []string {
	out := make([]string, 0, len(sig.Params))
	for _, p := range sig.Params {
		out = append(out, propertyName(p.Name))
	}
	return out
}

// evalArg statically evaluates one argument. A nil value with a nil error
// means "explicitly null or undefined", which is treated as absent.
func evalArg(f *source.File, n *ts.Node, p sdkdetect.Param) (any, *hintError) {
	switch p.Kind {
	case sdkdetect.ParamExpr, sdkdetect.ParamClient:
		return f.Text(n), nil

	case sdkdetect.ParamString:
		if isNullish(n) {
			return nil, nil
		}
		s, ok := stringLiteral(f, n)
		if !ok {
			return nil, &hintError{
				fmt.Sprintf("%s must be a string literal, not %s "+
					"(SDK arguments are read at compile time and never executed)",
					propertyName(p.Name), describe(f, n)),
				int(n.StartByte()),
			}
		}
		return s, nil

	case sdkdetect.ParamBool:
		switch n.Kind() {
		case "true":
			return true, nil
		case "false":
			return false, nil
		}
		if isNullish(n) {
			return nil, nil
		}
		return nil, &hintError{
			fmt.Sprintf("%s must be true or false, not %s", propertyName(p.Name), describe(f, n)),
			int(n.StartByte()),
		}

	case sdkdetect.ParamStringList:
		if isNullish(n) {
			return nil, nil
		}
		if n.Kind() != "array" {
			return nil, &hintError{
				fmt.Sprintf("%s must be an array of string literals, not %s",
					propertyName(p.Name), describe(f, n)),
				int(n.StartByte()),
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
				if item.Kind() == "identifier" || item.Kind() == "member_expression" {
					out = append(out, f.Text(item))
					continue
				}
				return nil, &hintError{
					fmt.Sprintf("%s entries must be string literals or names, not %s",
						propertyName(p.Name), describe(f, item)),
					int(item.StartByte()),
				}
			}
			out = append(out, s)
		}
		return out, nil
	}
	return nil, nil
}

// isNullish reports an explicitly absent value, which is treated the same as
// omitting the argument.
func isNullish(n *ts.Node) bool {
	return n.Kind() == "null" || n.Kind() == "undefined"
}

func describe(f *source.File, n *ts.Node) string {
	switch n.Kind() {
	case "identifier":
		return "the variable " + f.Text(n)
	case "call_expression":
		return "a function call"
	case "member_expression":
		return "a property lookup"
	case "template_string":
		return "a template literal with substitutions"
	case "binary_expression":
		return "an expression"
	case "number":
		return "a number"
	case "object":
		return "an object"
	case "array":
		return "an array"
	case "ternary_expression":
		return "a conditional expression"
	}
	return n.Kind()
}

// assignedName returns the binding a call's result is assigned to, which is
// what later passes match method calls against.
func assignedName(f *source.File, call *ts.Node) string {
	parent := call.Parent()
	if parent == nil {
		return ""
	}
	switch parent.Kind() {
	case "variable_declarator":
		if name := parent.ChildByFieldName("name"); name != nil && name.Kind() == "identifier" {
			return f.Text(name)
		}
	case "assignment_expression":
		if left := parent.ChildByFieldName("left"); left != nil && left.Kind() == "identifier" {
			return f.Text(left)
		}
	}
	return ""
}

// enclosingFunction returns the name of the innermost function containing the
// call, or "" for a call at module top level.
func enclosingFunction(f *source.File, call *ts.Node) string {
	for n := call.Parent(); n != nil; n = n.Parent() {
		switch n.Kind() {
		case "function_declaration", "generator_function_declaration", "method_definition":
			if name := n.ChildByFieldName("name"); name != nil {
				return f.Text(name)
			}
			return "?"
		case "function_expression", "arrow_function":
			// An arrow assigned to a name reads as that function.
			if p := n.Parent(); p != nil && p.Kind() == "variable_declarator" {
				if name := p.ChildByFieldName("name"); name != nil {
					return f.Text(name)
				}
			}
			return "?"
		}
	}
	return ""
}

func capabilityOf(fn string) string {
	sig, _ := sdkdetect.Lookup(fn)
	return sig.Capability
}

// literalString is a convenience for reading a plain string node, used for
// module specifiers where nothing more exotic is meaningful.
func literalString(f *source.File, n *ts.Node) string {
	s, _ := stringLiteral(f, n)
	return s
}

// stringLiteral decodes a JavaScript string, returning false when the value is
// not knowable without running the program.
//
// One decoder, used by both hint arguments and route paths. The Python
// frontend once had two and they drifted, silently losing routes.
func stringLiteral(f *source.File, n *ts.Node) (string, bool) {
	switch n.Kind() {
	case "string":
		var b strings.Builder
		for i := uint(0); i < n.NamedChildCount(); i++ {
			c := n.NamedChild(i)
			switch c.Kind() {
			case "string_fragment":
				b.WriteString(f.Text(c))
			case "escape_sequence":
				b.WriteString(unescape(f.Text(c)))
			}
		}
		return b.String(), true

	case "template_string":
		// A template literal is constant only when nothing is substituted into
		// it. `${x}` makes the value a runtime question.
		var b strings.Builder
		for i := uint(0); i < n.NamedChildCount(); i++ {
			c := n.NamedChild(i)
			switch c.Kind() {
			case "template_substitution":
				return "", false
			case "string_fragment":
				b.WriteString(f.Text(c))
			case "escape_sequence":
				b.WriteString(unescape(f.Text(c)))
			}
		}
		return b.String(), true

	case "parenthesized_expression":
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

	case "binary_expression":
		// "a" + "b" is how a long literal gets split, and a formatter produces
		// it. Both sides have to be constant.
		op := n.ChildByFieldName("operator")
		if op == nil || f.Text(op) != "+" {
			return "", false
		}
		left, lok := stringLiteral(f, n.ChildByFieldName("left"))
		right, rok := stringLiteral(f, n.ChildByFieldName("right"))
		if !lok || !rok {
			return "", false
		}
		return left + right, true
	}
	return "", false
}

// unescape resolves the escape sequences that appear in identifiers and paths.
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	if out, err := strconv.Unquote(`"` + strings.ReplaceAll(s, `"`, `\"`) + `"`); err == nil {
		return out
	}
	return s
}
