package fuzz

import (
	"fmt"
	"math/rand"
	"strings"
)

// The shapes in this file are the ones most likely to break something. The
// detector has to see through them, and the rewriter has to splice byte ranges
// out of them without disturbing anything around the call -- which is why
// comments inside argument lists, semicolons and unusual line endings are
// worth generating rather than assuming.

// bindingStyle is how a hint's result is bound to a name.
type bindingStyle int

const (
	bindPlain       bindingStyle = iota // pets = cc.persist_kv("x")
	bindAnnotated                       // pets: object = cc.persist_kv("x")
	bindSemicolon                       // pets = cc.persist_kv("x"); ready = True
	bindConditional                     // inside an `if` block
	bindClassAttr                       // as a class attribute
)

// bindStore writes a store declaration in one of several idiomatic bindings.
// The name must end up bound at module level either way, because that is what
// the importing units use.
func (m *pyModule) bindStore(rng *rand.Rand, target, expr string) {
	switch bindingStyle(rng.Intn(5)) {
	case bindAnnotated:
		m.linef("%s: object = %s", target, expr)

	case bindSemicolon:
		// Two statements on one line: the rewriter has to splice the call out
		// without disturbing what follows it on the same line.
		m.linef("%s = %s; %s_ready = True", target, expr, target)

	case bindConditional:
		m.line("if True:  # a feature flag in real code")
		m.linef("    %s = %s", target, indentContinuations(expr, "    "))

	case bindClassAttr:
		// A class body is still module-level execution, and people do group
		// their stores this way.
		cls := "_" + strings.ToUpper(target[:1]) + target[1:] + "Holder"
		m.linef("class %s:", cls)
		m.linef("    handle = %s", indentContinuations(expr, "    "))
		m.blank()
		m.linef("%s = %s.handle", target, cls)

	default:
		m.assign(target, expr)
	}
}

// indentContinuations re-indents the later lines of a multi-line expression so
// it stays valid inside a nested block.
func indentContinuations(expr, pad string) string {
	lines := strings.Split(expr, "\n")
	for i := 1; i < len(lines); i++ {
		lines[i] = pad + lines[i]
	}
	return strings.Join(lines, "\n")
}

// commentedCall renders a call with comments interleaved between arguments,
// which is common in configuration-heavy code and puts non-code bytes inside
// the span the rewriter replaces.
func (m *pyModule) commentedCall(callee string, args []arg) string {
	var b strings.Builder
	b.WriteString(callee)
	b.WriteString("(\n")
	for i, a := range args {
		b.WriteString("    # ")
		if a.name != "" {
			b.WriteString(a.name)
		} else {
			b.WriteString("positional")
		}
		b.WriteString("\n    ")
		if a.name != "" {
			b.WriteString(a.name)
			b.WriteString("=")
		}
		b.WriteString(a.value)
		if i < len(args)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString(")")
	return b.String()
}

// rawGlob writes a path as a raw string, which is what people reach for when a
// pattern contains backslashes and which the literal decoder must not mangle.
func rawGlob(rng *rand.Rand, glob string) (string, bool) {
	if rng.Intn(3) != 0 {
		return "", false
	}
	return `r"` + glob + `"`, true
}

// routeSet returns the routes an exposed unit serves, varying the paths people
// actually write: versioned prefixes, hyphens, and trailing segments.
func routeSet(rng *rand.Rand) (prefix string) {
	switch rng.Intn(4) {
	case 0:
		return "/items"
	case 1:
		return "/v1/items"
	case 2:
		return "/api/records"
	default:
		return "/data-items"
	}
}

// decorateHandler writes a route handler, sometimes stacked under a
// user-defined decorator and sometimes async.
func (m *pyModule) decorateHandler(rng *rand.Rand, appVar, verb, path, signature string, body []string) {
	stacked := rng.Intn(4) == 0
	if stacked {
		m.line("@_traced")
	}
	m.linef("@%s.%s(%s)", appVar, strings.ToLower(verb), m.quote(path))
	if rng.Intn(4) == 0 {
		m.linef("async def %s:", signature)
	} else {
		m.linef("def %s:", signature)
	}
	for _, l := range body {
		m.line("    " + l)
	}
}

// tracedDecorator is the user-defined decorator the stacked form refers to.
const tracedDecorator = `def _traced(fn):
    """A user decorator, stacked above the route decorator."""
    return fn`

// withCRLF rewrites a file to use Windows line endings. Files arrive from all
// sorts of places, and every byte offset the compiler records has to survive
// the extra carriage returns.
func withCRLF(src string) string {
	return strings.ReplaceAll(src, "\n", "\r\n")
}

// withTabs converts leading four-space indentation to tabs.
func withTabs(src string) string {
	lines := strings.Split(src, "\n")
	for i, l := range lines {
		n := 0
		for strings.HasPrefix(l[n:], "    ") {
			n += 4
		}
		if n > 0 {
			lines[i] = strings.Repeat("\t", n/4) + l[n:]
		}
	}
	return strings.Join(lines, "\n")
}

// trailingWhitespace sprinkles trailing spaces, which a byte-offset rewriter
// must simply ignore.
func trailingWhitespace(rng *rand.Rand, src string) string {
	lines := strings.Split(src, "\n")
	for i, l := range lines {
		if l != "" && rng.Intn(8) == 0 {
			lines[i] = l + "  "
		}
	}
	return strings.Join(lines, "\n")
}

// unusedSDKImport adds an SDK import to a module that makes no hint calls. The
// compiler still has to strip it, because the SDK is not installed in a
// deployment bundle.
func unusedSDKImport(rng *rand.Rand) string {
	if rng.Intn(3) != 0 {
		return ""
	}
	return fmt.Sprintf("import cloudcompiler as %s  # imported, never used here",
		moduleAliases[rng.Intn(len(moduleAliases))])
}
