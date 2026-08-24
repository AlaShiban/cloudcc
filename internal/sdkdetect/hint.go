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
const (
	FnExecutionUnit = "execution_unit"
	FnExpose        = "expose"
	FnPersistKV     = "persist_kv"
	FnPersistFS     = "persist_fs"
	FnPersistSecret = "persist_secret"
	FnPersistORM    = "persist_orm"
	FnPersistRedis  = "persist_redis"
	FnPubSubTopic   = "pubsub_topic"
	FnConfigValue   = "config_value"
	FnStaticUnit    = "static_unit"
	FnEmbedAssets   = "embed_assets"
)

// CapEmbedAssets is the pseudo-capability for embed_assets, which claims files
// but produces no intent of its own.
const CapEmbedAssets = "embed_assets"

// paramKind says how an argument is validated.
type paramKind int

const (
	// pString requires a string literal.
	pString paramKind = iota
	// pBool requires True or False.
	pBool
	// pStringList requires a list of string literals, or None.
	pStringList
	// pExpr accepts any expression; the source text is recorded verbatim.
	pExpr
)

type param struct {
	Name     string
	Kind     paramKind
	Required bool
}

type signature struct {
	// Capability is the config kind this call contributes to.
	Capability string
	Params     []param
}

// signatures mirrors sdk/python/cloudcompiler/__init__.py. The SDK parity test
// keeps the two in step.
var signatures = map[string]signature{
	FnExecutionUnit: {config.KindExecutionUnit, []param{
		{Name: "id", Kind: pString, Required: true},
		{Name: "type", Kind: pString},
	}},
	FnExpose: {config.KindExpose, []param{
		{Name: "app", Kind: pExpr, Required: true},
		{Name: "id", Kind: pString},
		{Name: "target", Kind: pString},
	}},
	FnPersistKV:     {config.KindPersistKV, []param{{Name: "id", Kind: pString, Required: true}}},
	FnPersistFS:     {config.KindPersistFS, []param{{Name: "id", Kind: pString, Required: true}}},
	FnPersistSecret: {config.KindPersistSecret, []param{{Name: "id", Kind: pString, Required: true}}},
	FnPersistORM: {config.KindPersistORM, []param{
		{Name: "id", Kind: pString, Required: true},
		{Name: "models", Kind: pStringList},
	}},
	FnPersistRedis: {config.KindPersistRedis, []param{{Name: "id", Kind: pString, Required: true}}},
	FnPubSubTopic:  {config.KindPubSub, []param{{Name: "id", Kind: pString, Required: true}}},
	FnConfigValue: {config.KindConfig, []param{
		{Name: "id", Kind: pString, Required: true},
		{Name: "default", Kind: pString},
		{Name: "secret", Kind: pBool},
	}},
	FnStaticUnit: {config.KindStaticUnit, []param{
		{Name: "id", Kind: pString, Required: true},
		{Name: "static_files", Kind: pString, Required: true},
		{Name: "index_document", Kind: pString},
		{Name: "shared_files", Kind: pString},
	}},
	FnEmbedAssets: {CapEmbedAssets, []param{{Name: "pattern", Kind: pString, Required: true}}},
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
	// Capability is the config kind the call contributes to, e.g. "persist_kv"
	// for persist_kv and "pubsub" for pubsub_topic.
	Capability string
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
