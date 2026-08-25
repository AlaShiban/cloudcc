package node

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cloudcompiler/cloudcc/internal/config"
	"github.com/cloudcompiler/cloudcc/internal/sdkdetect"
	"github.com/cloudcompiler/cloudcc/internal/source"
)

// RuntimePackage is the injected runtime's import specifier, relative to a
// unit's bundle root.
const RuntimePackage = "_cloudcc_runtime"

// shimTarget describes what one SDK function is rewritten into.
type shimTarget struct {
	// Module is the runtime submodule, "" when the call is erased.
	Module string
	// Alias is the local binding the module is imported as.
	Alias string
	// Call is the function called on the module.
	Call string
	// Args names the hint arguments passed through, in order.
	Args []string
	// Erase replaces the call with this literal instead of a shim call.
	Erase string
}

// shims maps a capability to the runtime module that connects to it, keyed by
// capability because one verb now covers every store.
var shims = map[string]shimTarget{
	config.KindPersistKV:     {Module: "kv", Alias: "_cloudccKv"},
	config.KindPersistFS:     {Module: "fs", Alias: "_cloudccFs"},
	config.KindPersistSecret: {Module: "secret", Alias: "_cloudccSecret"},
	config.KindPubSub:        {Module: "pubsub", Alias: "_cloudccPubsub"},
}

// libraryShims are the capabilities where the client library, not the
// capability, picks the module.
//
// Each of these modules imports exactly one package. That is what lets the
// bundler pull in ioredis or node-redis but never both, and it is why connect
// can stay synchronous: a single static import needs no await, and every one
// of these libraries connects lazily.
var libraryShims = map[string]shimTarget{
	sdkdetect.LibIORedis:   {Module: "redis_ioredis", Alias: "_cloudccRedis"},
	sdkdetect.LibNodeRedis: {Module: "redis_node", Alias: "_cloudccRedis"},
	sdkdetect.LibPg:        {Module: "orm_pg", Alias: "_cloudccOrm"},
	sdkdetect.LibKnex:      {Module: "orm_knex", Alias: "_cloudccOrm"},
}

// verbShims maps the verbs that name their own capability.
var verbShims = map[string]shimTarget{
	sdkdetect.FnConfigValue:   {Module: "config", Alias: "_cloudccConfig", Call: "value", Args: []string{"id", "default"}},
	sdkdetect.FnExpose:        {Module: "expose", Alias: "_cloudccExpose", Call: "register", Args: []string{"app", "id", "target"}},
	sdkdetect.FnExecutionUnit: {Erase: "undefined"},
	sdkdetect.FnStaticUnit:    {Erase: "undefined"},
	sdkdetect.FnEmbedAssets:   {Erase: "pattern"},
}

// shimFor returns the rewrite target for a hint.
func shimFor(h sdkdetect.Hint) (shimTarget, bool) {
	if h.Func != sdkdetect.FnPersist {
		target, ok := verbShims[h.Func]
		return target, ok
	}
	// A declared library picks the module; the capabilities this SDK supplies
	// a class for have no library and fall back to one module each.
	target, ok := libraryShims[h.ClientLibrary]
	if !ok {
		if target, ok = shims[h.Capability]; !ok {
			return shimTarget{}, false
		}
	}
	// Every store connects the same way: by id, returning a client of the same
	// type the program declared.
	target.Call = "connect"
	target.Args = []string{"id"}
	return target, true
}

// withLibrary returns h with its client library exposed as an argument, so the
// generic argument renderer emits it without needing a special case.
//
// The map is copied rather than written through: hints are shared between the
// plugins that read them, and a rewrite quietly editing one would be a bug
// found a long way from here.
func withLibrary(h sdkdetect.Hint) sdkdetect.Hint {
	if h.Func != sdkdetect.FnPersist || h.ClientLibrary == "" {
		return h
	}
	args := make(map[string]any, len(h.Args)+1)
	for k, v := range h.Args {
		args[k] = v
	}
	args["library"] = h.ClientLibrary
	h.Args = args
	return h
}

// rewrite replaces every SDK hint call with its runtime equivalent and removes
// the SDK import.
//
// It runs for every source file, including those with no hints: a module may
// import the SDK for a type annotation, or keep an import it no longer uses,
// and the SDK is not installed in a deployment bundle. Leaving one behind
// means the unit dies on its first import.
func rewrite(f *source.File, hints []sdkdetect.Hint, esm bool) error {
	if !f.Parsed() {
		return nil
	}
	imp := resolveImports(f)
	if !imp.any() && len(hints) == 0 {
		return nil
	}

	type splice struct {
		start, end int
		text       string
	}
	var splices []splice
	needed := map[string]string{}

	for _, h := range hints {
		if h.File != f.Path {
			continue
		}
		target, ok := shimFor(h)
		if !ok {
			continue
		}
		if target.Module == "" {
			text := target.Erase
			if h.Func == sdkdetect.FnEmbedAssets {
				text = jsString(h.Str("pattern"))
			}
			splices = append(splices, splice{h.Span[0], h.Span[1], text})
			continue
		}
		needed[target.Alias] = target.Module
		splices = append(splices, splice{h.Span[0], h.Span[1], renderCall(target, withLibrary(h))})
	}

	for _, span := range imp.spans {
		splices = append(splices, splice{span[0], span[1], ""})
	}
	if len(splices) == 0 {
		return nil
	}
	sort.Slice(splices, func(i, j int) bool { return splices[i].start > splices[j].start })

	content := append([]byte(nil), f.Content...)
	for _, s := range splices {
		if s.start < 0 || s.end > len(content) || s.start > s.end {
			return fmt.Errorf("%s: rewrite span %d:%d is out of range", f.Path, s.start, s.end)
		}
		out := make([]byte, 0, len(content)-(s.end-s.start)+len(s.text))
		out = append(out, content[:s.start]...)
		out = append(out, s.text...)
		out = append(out, content[s.end:]...)
		content = out
	}

	if len(needed) > 0 {
		content = insertImports(content, needed, esm, f.Path)
	}
	return f.SetContent(content)
}

// renderCall builds the shim call that replaces one hint. The first declared
// argument is positional and the rest become an options object, which is how
// the SDK is written and so how the runtime mirrors it.
func renderCall(target shimTarget, h sdkdetect.Hint) string {
	var positional string
	var options []string

	for i, name := range target.Args {
		v, ok := h.Args[name]
		if !ok {
			continue
		}
		var literal string
		switch typed := v.(type) {
		case string:
			if name == "app" {
				literal = typed // an expression, not a literal
			} else {
				literal = jsString(typed)
			}
		case bool:
			literal = fmt.Sprintf("%t", typed)
		default:
			continue
		}
		if i == 0 {
			positional = literal
			continue
		}
		options = append(options, propertyName(name)+": "+literal)
	}

	args := positional
	if len(options) > 0 {
		if args != "" {
			args += ", "
		}
		args += "{ " + strings.Join(options, ", ") + " }"
	}
	return fmt.Sprintf("%s.%s(%s)", target.Alias, target.Call, args)
}

// insertImports adds the runtime imports at the top of the module, in whichever
// module system the unit uses. Which system that is was decided once, from
// package.json and the file extension, rather than re-derived here.
//
// path is the importing file's location within the unit, which decides how many
// levels up the injected runtime is. A JavaScript import is resolved relative
// to the importing file, so a module in a subdirectory needs "../" for each
// level; Python never needed this because its imports are package-absolute.
func insertImports(content []byte, needed map[string]string, esm bool, path string) []byte {
	aliases := make([]string, 0, len(needed))
	for alias := range needed {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)

	prefix := runtimePrefix(path)

	var block strings.Builder
	block.WriteString("// Injected by cloudcc: runtime clients for this program's declared capabilities.\n")
	for _, alias := range aliases {
		if esm {
			fmt.Fprintf(&block, "import * as %s from \"%s%s/%s.js\";\n",
				alias, prefix, RuntimePackage, needed[alias])
		} else {
			fmt.Fprintf(&block, "const %s = require(\"%s%s/%s.js\");\n",
				alias, prefix, RuntimePackage, needed[alias])
		}
	}
	block.WriteString("\n")

	at := insertionPoint(content)
	out := make([]byte, 0, len(content)+block.Len())
	out = append(out, content[:at]...)
	out = append(out, block.String()...)
	out = append(out, content[at:]...)
	return out
}

// insertionPoint returns the offset after any leading directive prologue --
// "use strict" and friends -- which has to stay first to have any effect.
func insertionPoint(content []byte) int {
	at := 0
	for _, line := range strings.SplitAfter(string(content), "\n") {
		trimmed := strings.TrimSpace(line)
		isDirective := strings.HasPrefix(trimmed, `"use `) || strings.HasPrefix(trimmed, `'use `)
		isComment := strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") ||
			strings.HasPrefix(trimmed, "*")
		if trimmed == "" || isDirective || isComment {
			at += len(line)
			continue
		}
		break
	}
	return at
}

// runtimePrefix is the relative path from a file to its unit's root, where the
// injected runtime lives: "./" at the root, "../" one level down, and so on.
func runtimePrefix(path string) string {
	depth := strings.Count(filepath.ToSlash(path), "/")
	if depth == 0 {
		return "./"
	}
	return strings.Repeat("../", depth)
}

func jsString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\t", `\t`).Replace(s) + `"`
}
