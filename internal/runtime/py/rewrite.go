// Package py injects the runtime shims and rewrites SDK hint calls in the
// compiled copy of a program (D13).
//
// The user's source tree is never modified. Rewriting is a byte-range splice
// at each hint's recorded span followed by a reparse, so successive rewrites
// stay consistent with the AST -- the same approach Klotho 1 used, and the
// reason hints carry byte spans at all.
package py

import (
	"fmt"
	"sort"
	"strings"

	"github.com/cloudcompiler/cc/internal/sdkdetect"
	"github.com/cloudcompiler/cc/internal/source"
	ts "github.com/tree-sitter/go-tree-sitter"
)

// RuntimePackage is the injected package's name.
const RuntimePackage = "_cc_runtime"

// shimTarget describes what one SDK function is rewritten into.
type shimTarget struct {
	// Module is the _cc_runtime submodule, "" when the call is erased.
	Module string
	// Alias is the local name the module is imported as.
	Alias string
	// Call is the function called on the module.
	Call string
	// Args names the hint arguments passed through, in order.
	Args []string
	// Erase replaces the call with this literal instead of a shim call.
	Erase string
}

// shims maps every SDK function to its runtime counterpart. Compile-only
// hints -- execution_unit, static_unit -- are erased, because they describe
// the build rather than anything that happens at runtime.
var shims = map[string]shimTarget{
	sdkdetect.FnPersistKV:     {Module: "kv", Alias: "_cc_kv", Call: "connect", Args: []string{"id"}},
	sdkdetect.FnPersistFS:     {Module: "fs", Alias: "_cc_fs", Call: "connect", Args: []string{"id"}},
	sdkdetect.FnPersistSecret: {Module: "secret", Alias: "_cc_secret", Call: "connect", Args: []string{"id"}},
	sdkdetect.FnPersistORM:    {Module: "orm", Alias: "_cc_orm", Call: "connect", Args: []string{"id"}},
	sdkdetect.FnPersistRedis:  {Module: "redis_", Alias: "_cc_redis", Call: "connect", Args: []string{"id"}},
	sdkdetect.FnPubSubTopic:   {Module: "pubsub", Alias: "_cc_pubsub", Call: "connect", Args: []string{"id"}},
	sdkdetect.FnConfigValue:   {Module: "config", Alias: "_cc_config", Call: "value", Args: []string{"id", "default"}},
	sdkdetect.FnExpose:        {Module: "expose", Alias: "_cc_expose", Call: "register", Args: []string{"app", "id", "target"}},
	sdkdetect.FnExecutionUnit: {Erase: "None"},
	sdkdetect.FnStaticUnit:    {Erase: "None"},
	sdkdetect.FnEmbedAssets:   {Erase: "pattern"},
}

// Rewrite replaces every SDK hint call in f with its runtime equivalent and
// swaps the SDK import for the shim imports the file now needs. It is a no-op
// for files that do not import the SDK.
func Rewrite(f *source.File, hints []sdkdetect.Hint) error {
	if !f.IsPython() {
		return nil
	}
	imports := sdkdetect.ResolveImports(f)
	if !imports.Any() && len(hints) == 0 {
		return nil
	}

	type splice struct {
		start, end int
		text       string
	}
	var splices []splice
	needed := map[string]string{} // alias -> module

	for _, h := range hints {
		if h.File != f.Path {
			continue
		}
		target, ok := shims[h.Func]
		if !ok {
			continue
		}
		if target.Module == "" {
			text := target.Erase
			if h.Func == sdkdetect.FnEmbedAssets {
				text = pyString(h.Str("pattern"))
			}
			splices = append(splices, splice{h.Span[0], h.Span[1], text})
			continue
		}
		needed[target.Alias] = target.Module
		splices = append(splices, splice{h.Span[0], h.Span[1], renderCall(target, h)})
	}

	// The SDK is compile-time only and is not installed in the bundle, so its
	// import statements go away with the calls they served.
	for _, span := range sdkImportSpans(f) {
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
		content = insertImports(content, needed)
	}
	return f.SetContent(content)
}

// renderCall builds the shim call that replaces one hint. The first declared
// argument is passed positionally and the rest by keyword, which keeps the
// generated call readable and immune to an omitted optional in the middle.
func renderCall(target shimTarget, h sdkdetect.Hint) string {
	var args []string
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
				literal = pyString(typed)
			}
		case bool:
			literal = pyBool(typed)
		default:
			continue
		}
		if i == 0 {
			args = append(args, literal)
		} else {
			args = append(args, name+"="+literal)
		}
	}
	return fmt.Sprintf("%s.%s(%s)", target.Alias, target.Call, strings.Join(args, ", "))
}

// sdkImportSpans returns the byte ranges of the SDK import statements,
// extended to swallow the trailing newline so no blank line is left behind.
func sdkImportSpans(f *source.File) [][2]int {
	root := f.Root()
	if root == nil {
		return nil
	}
	var spans [][2]int
	var walk func(n *ts.Node)
	walk = func(n *ts.Node) {
		kind := n.Kind()
		if kind == "import_statement" || kind == "import_from_statement" {
			if strings.Contains(f.Text(n), sdkdetect.PackageName) {
				start, end := int(n.StartByte()), int(n.EndByte())
				if end < len(f.Content) && f.Content[end] == '\n' {
					end++
				}
				spans = append(spans, [2]int{start, end})
			}
			return
		}
		for i := uint(0); i < n.NamedChildCount(); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(root)
	return spans
}

// insertImports adds the shim imports after the module docstring and any
// __future__ imports, which must stay first.
func insertImports(content []byte, needed map[string]string) []byte {
	aliases := make([]string, 0, len(needed))
	for alias := range needed {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)

	var block strings.Builder
	block.WriteString("# Injected by cc: runtime clients for this program's declared capabilities.\n")
	for _, alias := range aliases {
		fmt.Fprintf(&block, "from %s import %s as %s\n", RuntimePackage, needed[alias], alias)
	}
	block.WriteString("\n")

	at := insertionPoint(content)
	out := make([]byte, 0, len(content)+block.Len())
	out = append(out, content[:at]...)
	out = append(out, block.String()...)
	out = append(out, content[at:]...)
	return out
}

// insertionPoint returns the byte offset after the module docstring and any
// leading __future__ imports.
func insertionPoint(content []byte) int {
	f := &source.File{Path: "<rewrite>", Content: content}
	if err := f.ParsePython(); err != nil {
		return 0
	}
	defer f.SetContent(nil)

	root := f.Root()
	if root == nil {
		return 0
	}
	at := 0
	for i := uint(0); i < root.NamedChildCount(); i++ {
		child := root.NamedChild(i)
		text := f.Text(child)
		isDocstring := i == 0 && child.Kind() == "expression_statement" &&
			child.NamedChildCount() == 1 && child.NamedChild(0).Kind() == "string"
		// tree-sitter gives __future__ imports their own node kind; they must
		// stay the first statement in the file.
		isFuture := child.Kind() == "future_import_statement" ||
			(child.Kind() == "import_from_statement" && strings.Contains(text, "__future__"))
		if !isDocstring && !isFuture {
			break
		}
		at = int(child.EndByte())
		if at < len(content) && content[at] == '\n' {
			at++
		}
	}
	return at
}

func pyString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\t", `\t`).Replace(s) + `"`
}

func pyBool(b bool) string {
	if b {
		return "True"
	}
	return "False"
}
