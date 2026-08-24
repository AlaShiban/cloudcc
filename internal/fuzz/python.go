package fuzz

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
)

// This file is the coverage surface. Everything here exists to write the same
// meaning in as many shapes as a real person might, because the compiler reads
// syntax rather than running code: a hint it cannot see is a resource that
// silently does not exist.

// importStyle is how a module refers to the SDK.
type importStyle int

const (
	// styleAlias: import cloudcompiler as cloudcc
	styleAlias importStyle = iota
	// stylePlain: import cloudcompiler
	stylePlain
	// styleFrom: from cloudcompiler import persist_kv
	styleFrom
	// styleFromAliased: from cloudcompiler import persist_kv as kv_store
	styleFromAliased
)

var allImportStyles = []importStyle{styleAlias, stylePlain, styleFrom, styleFromAliased}

// moduleAliases are plausible names a person would bind the package to.
var moduleAliases = []string{"cloudcc", "cc_sdk", "compiler", "infra", "cloudcompiler"}

// pyModule accumulates one generated Python file. SDK imports are collected as
// calls are written and rendered at the top afterwards, which is what lets the
// import style vary independently of the body.
type pyModule struct {
	rng *rand.Rand

	docstring bool
	future    bool
	style     importStyle
	alias     string

	// needed maps an SDK function to the local name this module calls it by.
	needed map[string]string
	// stdImports are non-SDK imports, rendered before the SDK ones.
	stdImports []string
	// localImports are imports of other generated modules.
	localImports []string

	body []string
}

func newModule(rng *rand.Rand) *pyModule {
	m := &pyModule{
		rng:    rng,
		needed: map[string]string{},
	}
	m.docstring = rng.Intn(3) > 0
	m.future = rng.Intn(4) == 0
	m.style = allImportStyles[rng.Intn(len(allImportStyles))]
	m.alias = moduleAliases[rng.Intn(len(moduleAliases))]
	if m.style == stylePlain {
		m.alias = "cloudcompiler"
	}
	return m
}

// localName returns the name this module calls an SDK function by, registering
// the import that makes it available.
func (m *pyModule) localName(fn string) string {
	if name, ok := m.needed[fn]; ok {
		return name
	}
	var name string
	switch m.style {
	case styleAlias, stylePlain:
		name = m.alias + "." + fn
	case styleFrom:
		name = fn
	case styleFromAliased:
		name = directAlias(fn)
	}
	m.needed[fn] = name
	return name
}

// directAlias is the local name a from-import binds a function to, chosen to
// look like something a person would actually write.
func directAlias(fn string) string {
	switch fn {
	case "persist_kv":
		return "kv_store"
	case "persist_fs":
		return "file_store"
	case "persist_secret":
		return "secret_store"
	case "persist_orm":
		return "database"
	case "persist_redis":
		return "cache_store"
	case "pubsub_topic":
		return "topic"
	case "config_value":
		return "setting"
	case "expose":
		return "publish_app"
	case "execution_unit":
		return "unit"
	case "static_unit":
		return "site"
	case "embed_assets":
		return "assets"
	}
	return fn
}

// arg is one rendered call argument.
type arg struct {
	// name is the keyword, empty for a positional argument.
	name string
	// value is already-rendered Python source.
	value string
}

func pos(value string) arg      { return arg{value: value} }
func kw(name, value string) arg { return arg{name: name, value: value} }

// quoteStyle renders a Python string literal in one of the several ways a
// person writes one. Every form here must decode to exactly s.
func (m *pyModule) quote(s string) string {
	switch m.rng.Intn(6) {
	case 0:
		return `'` + escapeFor(s, '\'') + `'`
	case 1:
		return `"""` + escapeFor(s, '"') + `"""`
	case 2:
		// Implicit concatenation, which the compiler has to join back up.
		if len(s) > 3 {
			cut := 1 + m.rng.Intn(len(s)-2)
			return `"` + escapeFor(s[:cut], '"') + `" "` + escapeFor(s[cut:], '"') + `"`
		}
	case 3:
		// Parenthesised, spanning lines.
		if len(s) > 3 {
			cut := 1 + m.rng.Intn(len(s)-2)
			return "(\n        \"" + escapeFor(s[:cut], '"') + "\"\n        \"" +
				escapeFor(s[cut:], '"') + "\"\n    )"
		}
	}
	return `"` + escapeFor(s, '"') + `"`
}

func escapeFor(s string, q byte) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\':
			b.WriteString(`\\`)
		case c == q:
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// renderCall lays a call out in one of several shapes, including the
// multi-line and trailing-comma forms that make byte-span rewriting
// interesting.
func (m *pyModule) renderCall(callee string, args []arg) string {
	parts := make([]string, 0, len(args))
	for _, a := range args {
		if a.name == "" {
			parts = append(parts, a.value)
		} else {
			parts = append(parts, a.name+"="+a.value)
		}
	}
	joined := strings.Join(parts, ", ")

	// A multi-line layout is only worth choosing when there is more than one
	// argument; otherwise it reads as noise rather than as idiom.
	if len(parts) > 1 && m.rng.Intn(4) == 0 {
		var b strings.Builder
		b.WriteString(callee)
		b.WriteString("(\n")
		for i, p := range parts {
			b.WriteString("    ")
			b.WriteString(p)
			if i < len(parts)-1 || m.rng.Intn(2) == 0 {
				b.WriteString(",")
			}
			b.WriteString("\n")
		}
		b.WriteString(")")
		return b.String()
	}
	if len(parts) > 0 && m.rng.Intn(8) == 0 {
		// Spaces inside the parentheses: unusual, still valid, and a good way
		// to catch a rewriter that assumes tight formatting.
		return callee + "( " + joined + " )"
	}
	return callee + "(" + joined + ")"
}

// hint renders an SDK call and returns the expression source.
func (m *pyModule) hint(fn string, args ...arg) string {
	return m.renderCall(m.localName(fn), args)
}

// assign writes `target = <call>`, sometimes with a trailing comment or a
// second statement on the same line.
func (m *pyModule) assign(target, expr string) {
	line := target + " = " + expr
	switch m.rng.Intn(10) {
	case 0:
		line += "  # wired by the compiler"
	case 1:
		m.line("# " + target + " is resolved at compile time")
	}
	m.line(line)
}

func (m *pyModule) line(s string)            { m.body = append(m.body, s) }
func (m *pyModule) linef(f string, a ...any) { m.body = append(m.body, fmt.Sprintf(f, a...)) }
func (m *pyModule) blank()                   { m.body = append(m.body, "") }

func (m *pyModule) importStd(spec string) {
	for _, existing := range m.stdImports {
		if existing == spec {
			return
		}
	}
	m.stdImports = append(m.stdImports, spec)
}

func (m *pyModule) importLocal(spec string) {
	for _, existing := range m.localImports {
		if existing == spec {
			return
		}
	}
	m.localImports = append(m.localImports, spec)
}

// render assembles the file: docstring, __future__, third-party imports, the
// SDK import, local imports, then the body.
func (m *pyModule) render(doc string) string {
	var b strings.Builder

	if m.docstring {
		if strings.Contains(doc, "\n") {
			b.WriteString(`"""` + doc + `"""` + "\n\n")
		} else {
			b.WriteString(`"""` + doc + `"""` + "\n\n")
		}
	}
	if m.future {
		b.WriteString("from __future__ import annotations\n\n")
	}
	for _, spec := range m.stdImports {
		b.WriteString(spec + "\n")
	}
	if len(m.stdImports) > 0 {
		b.WriteString("\n")
	}
	if imp := m.renderSDKImport(); imp != "" {
		b.WriteString(imp + "\n\n")
	}
	for _, spec := range m.localImports {
		b.WriteString(spec + "\n")
	}
	if len(m.localImports) > 0 {
		b.WriteString("\n")
	}

	b.WriteString(strings.Join(m.body, "\n"))
	if !strings.HasSuffix(b.String(), "\n") {
		b.WriteString("\n")
	}
	return b.String()
}

func (m *pyModule) renderSDKImport() string {
	if len(m.needed) == 0 {
		return ""
	}
	switch m.style {
	case stylePlain:
		return "import cloudcompiler"
	case styleAlias:
		return "import cloudcompiler as " + m.alias
	}

	fns := make([]string, 0, len(m.needed))
	for fn := range m.needed {
		fns = append(fns, fn)
	}
	sort.Strings(fns)

	specs := make([]string, 0, len(fns))
	for _, fn := range fns {
		if m.style == styleFromAliased {
			specs = append(specs, fn+" as "+directAlias(fn))
		} else {
			specs = append(specs, fn)
		}
	}
	// A long from-import gets wrapped in parentheses, as a formatter would.
	if len(specs) > 2 {
		return "from cloudcompiler import (\n    " + strings.Join(specs, ",\n    ") + ",\n)"
	}
	return "from cloudcompiler import " + strings.Join(specs, ", ")
}

// ---------------------------------------------------------------- ids

// idShapes produce identifiers that stress the per-service name sanitisers:
// case, punctuation, digits and length all have to survive into valid AWS
// names without two distinct ids ever colliding.
var idShapes = []func(rng *rand.Rand, base string, n int) string{
	func(_ *rand.Rand, base string, n int) string { return fmt.Sprintf("%s%d", base, n) },
	func(_ *rand.Rand, base string, n int) string { return fmt.Sprintf("%s-%d", base, n) },
	func(_ *rand.Rand, base string, n int) string { return fmt.Sprintf("%s_%d", base, n) },
	func(_ *rand.Rand, base string, n int) string { return fmt.Sprintf("%s.%d", base, n) },
	func(_ *rand.Rand, base string, n int) string {
		return fmt.Sprintf("%sBy%d", base, n)
	},
	func(_ *rand.Rand, base string, n int) string {
		return fmt.Sprintf("%s%d%s", strings.ToUpper(base[:1]), n, base[1:])
	},
}

func makeID(rng *rand.Rand, base string, n int) string {
	return idShapes[rng.Intn(len(idShapes))](rng, base, n)
}

// pyIdentifier turns an id into something usable as a Python variable.
func pyIdentifier(id string) string {
	var b strings.Builder
	upper := false
	for i, r := range id {
		switch {
		case r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z':
			if upper {
				b.WriteRune(r - 32*boolToRune(r >= 'a' && r <= 'z'))
				upper = false
			} else if i == 0 {
				b.WriteRune(r)
			} else {
				b.WriteRune(r)
			}
		case r >= '0' && r <= '9':
			if b.Len() == 0 {
				b.WriteString("v")
			}
			b.WriteRune(r)
		default:
			upper = true
			b.WriteByte('_')
		}
	}
	out := strings.ReplaceAll(b.String(), "__", "_")
	out = strings.Trim(out, "_")
	if out == "" {
		return "handle"
	}
	return strings.ToLower(out[:1]) + out[1:]
}

func boolToRune(b bool) rune {
	if b {
		return 1
	}
	return 0
}
