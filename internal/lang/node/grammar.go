package node

import (
	"strings"
	"sync"

	"github.com/cloudcompiler/cloudcc/internal/source"
	ts "github.com/tree-sitter/go-tree-sitter"
	tsjs "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	tsts "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
)

// Extensions this frontend claims. `.d.ts` is deliberately excluded further
// down: a declaration file states types it does not implement, and bundling
// one would ship a module that shadows the real thing.
var extensions = []string{".js", ".mjs", ".cjs", ".jsx", ".ts", ".mts", ".cts", ".tsx"}

var (
	jsOnce, tsOnce, tsxOnce sync.Once
	jsLang, tsLang, tsxLang *ts.Language
)

func javascriptLanguage() *ts.Language {
	jsOnce.Do(func() { jsLang = ts.NewLanguage(tsjs.Language()) })
	return jsLang
}

func typescriptLanguage() *ts.Language {
	tsOnce.Do(func() { tsLang = ts.NewLanguage(tsts.LanguageTypescript()) })
	return tsLang
}

func tsxLanguage() *ts.Language {
	tsxOnce.Do(func() { tsxLang = ts.NewLanguage(tsts.LanguageTSX()) })
	return tsxLang
}

// grammarFor picks the grammar for a path. TypeScript is a superset of
// JavaScript for every shape this compiler reads, but the grammars differ on
// JSX, so the extension decides rather than a guess.
func grammarFor(path string) (string, *ts.Language) {
	switch {
	case strings.HasSuffix(path, ".tsx"):
		return "tsx", tsxLanguage()
	case strings.HasSuffix(path, ".jsx"):
		return "jsx", tsxLanguage()
	case strings.HasSuffix(path, ".ts"), strings.HasSuffix(path, ".mts"), strings.HasSuffix(path, ".cts"):
		return "typescript", typescriptLanguage()
	default:
		return "javascript", javascriptLanguage()
	}
}

// Queries are compiled per grammar, because a query is bound to the grammar it
// was compiled against.
type queries struct {
	call       *ts.Query
	member     *ts.Query
	identifier *ts.Query
}

var (
	queryOnce  sync.Once
	byGrammar  map[string]*queries
	grammarsBy = map[string]*ts.Language{}
)

func compiled() map[string]*queries {
	queryOnce.Do(func() {
		byGrammar = map[string]*queries{}
		for name, language := range map[string]*ts.Language{
			"javascript": javascriptLanguage(),
			"typescript": typescriptLanguage(),
			"tsx":        tsxLanguage(),
			"jsx":        tsxLanguage(),
		} {
			grammarsBy[name] = language
			byGrammar[name] = &queries{
				call: source.MustQuery(language, `(call_expression) @call`),
				member: source.MustQuery(language,
					`(call_expression function: (member_expression object: (identifier) @obj property: (property_identifier) @method)) @call`),
				identifier: source.MustQuery(language, `(identifier) @id`),
			}
		}
	})
	return byGrammar
}

// queriesFor returns the query set matching a parsed file's grammar.
func queriesFor(f *source.File) *queries {
	name, _ := grammarFor(f.Path)
	if q, ok := compiled()[name]; ok {
		return q
	}
	return compiled()["javascript"]
}

func queryFor(f *source.File) *ts.Query { return queriesFor(f).call }
