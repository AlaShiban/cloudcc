// Package sdkdetect finds CloudCompiler SDK calls in Python source.
//
// The SDK replaces Klotho 1's comment annotations (D1): a call like
// cloudcc.persist_kv("petsByOwner") is a compile-time hint. Detection is
// import-alias aware and accepts literal arguments only, so every capability
// is resolvable without executing a line of user code.
package sdkdetect

import (
	"fmt"
	"sort"

	"github.com/cloudcompiler/cloudcc/internal/config"
)

// PackageName is the importable name of the SDK.
const PackageName = "cloudcompiler"

// SDK function names.
//
// There is one verb per action rather than one per resource: what a call
// persists is decided by the type of the client handed to it, not by the name
// of the function. That is what lets a program bring the client it already
// uses and keep its type all the way through.
const (
	FnPersist       = "persist"
	FnRemote        = "remote"
	FnExecutionUnit = "execution_unit"
	FnExpose        = "expose"
	FnConfigValue   = "config_value"
	FnStaticUnit    = "static_unit"
	FnEmbedAssets   = "embed_assets"
)

// CapEmbedAssets is the pseudo-capability for embed_assets, which claims files
// but produces no intent of its own.
const CapEmbedAssets = "embed_assets"

// CapRemote is the pseudo-capability for remote. Like embed_assets it produces
// no intent of its own and so is not one of config.Kinds: the thing being
// called is an execution unit that already exists in the graph, and what a
// remote() call adds is an edge to it rather than a node. It is therefore also
// the one capability with nothing to configure -- there is no choice of
// backing service to make, because the callee's own configuration already made
// it.
const CapRemote = "remote"

// ParamKind says how an argument is validated. It is a property of the SDK's
// surface, not of any one language: an id is a string literal whether it is
// written in Python or TypeScript.
type ParamKind int

const (
	// ParamString requires a string literal.
	ParamString ParamKind = iota
	// ParamBool requires a boolean literal.
	ParamBool
	// ParamStringList requires a list of string literals, or null.
	ParamStringList
	// ParamExpr accepts any expression; the source text is recorded verbatim.
	ParamExpr
	// ParamClient is a client whose *type* declares the capability. The
	// argument is an expression, and the constructor at its head is what
	// decides whether this is a cache, a database or a bucket.
	ParamClient
)

// Param is one declared parameter of an SDK function.
type Param struct {
	Name     string
	Kind     ParamKind
	Required bool
	// KeywordOnly mirrors a `*` in the SDK's Python signature. It matters
	// because the compiler reads calls rather than running them: without it,
	// a spelling Python itself would reject at runtime would still compile,
	// and the program would work only after being compiled.
	KeywordOnly bool
}

// PositionalParams returns the parameters that may be passed positionally.
func PositionalParams(fn string) []Param {
	var out []Param
	for _, p := range signatures[fn].Params {
		if !p.KeywordOnly {
			out = append(out, p)
		}
	}
	return out
}

// Signature is an SDK function's shape.
type Signature struct {
	// Capability is the config kind this call contributes to.
	Capability string
	Params     []Param
}

// Lookup returns the signature of an SDK function.
func Lookup(fn string) (Signature, bool) {
	sig, ok := signatures[fn]
	return sig, ok
}

// signatures mirrors sdk/python/cloudcompiler/__init__.py. The SDK parity test
// keeps the two in step.
var signatures = map[string]Signature{
	// persist has no fixed capability: the client decides it, which is why
	// this entry leaves Capability empty for the detector to fill in.
	FnPersist: {"", []Param{
		{Name: "client", Kind: ParamClient, Required: true},
		{Name: "id", Kind: ParamString, Required: true, KeywordOnly: true},
		{Name: "models", Kind: ParamStringList, KeywordOnly: true},
	}},
	// remote names the unit being called, so its capability is fixed. The
	// target is an expression only so that the uncompiled program keeps
	// working: the compiler reads the id, never the module.
	FnRemote: {CapRemote, []Param{
		{Name: "target", Kind: ParamExpr, Required: true},
		{Name: "id", Kind: ParamString, Required: true, KeywordOnly: true},
	}},
	FnExecutionUnit: {config.KindExecutionUnit, []Param{
		{Name: "id", Kind: ParamString, Required: true},
		{Name: "type", Kind: ParamString},
	}},
	FnExpose: {config.KindExpose, []Param{
		{Name: "app", Kind: ParamExpr, Required: true},
		{Name: "id", Kind: ParamString},
		{Name: "target", Kind: ParamString},
	}},
	FnConfigValue: {config.KindConfig, []Param{
		{Name: "id", Kind: ParamString, Required: true},
		{Name: "default", Kind: ParamString},
		{Name: "secret", Kind: ParamBool},
	}},
	FnStaticUnit: {config.KindStaticUnit, []Param{
		{Name: "id", Kind: ParamString, Required: true},
		{Name: "static_files", Kind: ParamString, Required: true},
		{Name: "index_document", Kind: ParamString},
		{Name: "shared_files", Kind: ParamString},
	}},
	FnEmbedAssets: {CapEmbedAssets, []Param{{Name: "pattern", Kind: ParamString, Required: true}}},
}

// FunctionNames returns every SDK function name, sorted.
func FunctionNames() []string {
	out := make([]string, 0, len(signatures))
	for k := range signatures {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ParamNames returns the declared parameter names of an SDK function, in
// positional order.
func ParamNames(fn string) []string {
	sig, ok := signatures[fn]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(sig.Params))
	for _, p := range sig.Params {
		out = append(out, p.Name)
	}
	return out
}

// Hint is one detected SDK call.
type Hint struct {
	// Func is the SDK function name, e.g. "persist_kv".
	Func string
	// Capability is the config kind the call contributes to. For persist it is
	// resolved from the client's type rather than from the function name.
	Capability string
	// ClientType is the concrete resource the client asks for, e.g.
	// "elasticache" or "rds_mysql". It is the weakest layer of configuration:
	// the library a program reached for supplies the default, and cloudcc.yaml
	// still chooses between variants of it.
	ClientType string
	// ClientLibrary identifies which client library was wrapped -- "ioredis",
	// "sqlalchemy-async" and so on. The shim dispatches on it so it can hand
	// back a client of the same kind, and the bundle carries that library.
	//
	// The capability alone is not enough: two Redis libraries have different
	// APIs, and a synchronous SQLAlchemy engine is not an asynchronous one.
	// Returning the wrong one compiles cleanly and fails on the first call.
	//
	// Empty for the capabilities this SDK supplies a class for, where the
	// shim's own class is the only implementation there is.
	ClientLibrary string
	// Client is the source text of the wrapped client expression, kept so the
	// rewriter knows what it is replacing.
	Client string
	// ClientArgs holds the literal arguments of the wrapped constructor, for
	// the SDK-supplied clients whose arguments are declarations rather than
	// connection settings.
	//
	// A Topic's arguments are its requirements -- ordering, delivery, replay --
	// and the compiler chooses the backing service from them. Nothing reads the
	// arguments of a library's own client: `Redis(host=...)` is talking to the
	// local Redis, and what host it uses is none of the compiler's business.
	ClientArgs map[string]any
	// Args holds the resolved arguments by parameter name. String, bool and
	// list literals are stored as Go values; pExpr parameters are stored as
	// the verbatim source text of the expression.
	Args map[string]any
	// File is the source path relative to the root.
	File string
	// Span is the hint's byte range in File, used for diagnostics and for the
	// shim rewrite.
	Span [2]int
	// Receives is the identifier the call's result is assigned to, if any.
	Receives string
	// Enclosing is the name of the function the call appears in, or "" for a
	// module-level call.
	Enclosing string
}

// ID returns the hint's id argument.
func (h Hint) ID() string { return h.Str("id") }

// Str returns a string argument, or "" when absent.
func (h Hint) Str(name string) string {
	if v, ok := h.Args[name].(string); ok {
		return v
	}
	return ""
}

// Bool returns a boolean argument, or false when absent.
func (h Hint) Bool(name string) bool {
	if v, ok := h.Args[name].(bool); ok {
		return v
	}
	return false
}

// StrList returns a string-list argument, or nil when absent.
func (h Hint) StrList(name string) []string {
	if v, ok := h.Args[name].([]string); ok {
		return v
	}
	return nil
}

func (h Hint) String() string {
	return fmt.Sprintf("%s(%v) at %s:%d", h.Func, h.Args, h.File, h.Span[0])
}
