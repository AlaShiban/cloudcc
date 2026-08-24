package node

import (
	"reflect"
	"strings"
	"testing"

	"github.com/cloudcompiler/cloudcc/internal/diag"
	"github.com/cloudcompiler/cloudcc/internal/sdkdetect"
	"github.com/cloudcompiler/cloudcc/internal/source"
)

func detect(t *testing.T, path, src string) ([]sdkdetect.Hint, *diag.Diagnostics) {
	t.Helper()
	f := &source.File{Path: path, Content: []byte(src)}
	if err := (Frontend{}).Parse(f); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.SetContent(nil) })
	var d diag.Diagnostics
	return detectHints(f, &d), &d
}

func TestDetectNamedImport(t *testing.T) {
	hints, d := detect(t, "app.js", `
import { persist, KVStore } from "@cloudcompiler/sdk";
const pets = persist(new KVStore(), { id: "petsByOwner" });
`)
	if d.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", d.Items())
	}
	if len(hints) != 1 || hints[0].ID() != "petsByOwner" || hints[0].Receives != "pets" {
		t.Fatalf("hints = %+v", hints)
	}
}

func TestDetectAliasedImport(t *testing.T) {
	hints, _ := detect(t, "app.ts", `
import { persist as store, KVStore } from "@cloudcompiler/sdk";
const pets = store(new KVStore(), { id: "a" });
`)
	if len(hints) != 1 || hints[0].ID() != "a" {
		t.Fatalf("hints = %+v", hints)
	}
}

func TestDetectNamespaceImport(t *testing.T) {
	hints, _ := detect(t, "app.js", `
import * as sdk from "@cloudcompiler/sdk";
const pets = sdk.persist(new sdk.KVStore(), { id: "a" });
`)
	if len(hints) != 1 || hints[0].Func != sdkdetect.FnPersist {
		t.Fatalf("hints = %+v", hints)
	}
}

// Both module systems are idiomatic and a program may mix them.
func TestDetectRequireForms(t *testing.T) {
	cases := map[string]string{
		"destructured": `const { persist, KVStore } = require("@cloudcompiler/sdk");
const pets = persist(new KVStore(), { id: "a" });`,
		"namespace": `const sdk = require("@cloudcompiler/sdk");
const pets = sdk.persist(new sdk.KVStore(), { id: "a" });`,
		"renamed": `const { persist: kv, KVStore } = require("@cloudcompiler/sdk");
const pets = kv(new KVStore(), { id: "a" });`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			hints, d := detect(t, "app.cjs", src)
			if d.HasErrors() {
				t.Fatalf("diagnostics: %v", d.Items())
			}
			if len(hints) != 1 || hints[0].ID() != "a" {
				t.Fatalf("hints = %+v", hints)
			}
		})
	}
}

func TestOptionsObjectBecomesKeywordArguments(t *testing.T) {
	hints, d := detect(t, "app.ts", `
import { configValue, executionUnit, staticUnit } from "@cloudcompiler/sdk";
executionUnit({ id: "api" });
const level = configValue("log_level", { default: "info", secret: true });
staticUnit("site", { staticFiles: "./public/**/*", indexDocument: "home.html" });
`)
	if d.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", d.Items())
	}
	if len(hints) != 3 {
		t.Fatalf("hints = %+v", hints)
	}
	if hints[0].ID() != "api" {
		t.Errorf("executionUnit id = %q", hints[0].ID())
	}
	if hints[1].Str("default") != "info" || !hints[1].Bool("secret") {
		t.Errorf("configValue args = %+v", hints[1].Args)
	}
	// camelCase options map onto the shared parameter names.
	if hints[2].Str("static_files") != "./public/**/*" || hints[2].Str("index_document") != "home.html" {
		t.Errorf("staticUnit args = %+v", hints[2].Args)
	}
}

func TestExposeRecordsTheApplicationBinding(t *testing.T) {
	hints, _ := detect(t, "app.js", `
import express from "express";
import { expose } from "@cloudcompiler/sdk";
const app = express();
expose(app, { id: "pet-api" });
`)
	if len(hints) != 1 || hints[0].Str("app") != "app" || hints[0].Str("id") != "pet-api" {
		t.Fatalf("hints = %+v", hints)
	}
}

// The Python frontend rejected a parenthesised literal until a generated
// program found it; the same shapes are pre-empted here rather than
// rediscovered.
func TestStringLiteralForms(t *testing.T) {
	cases := map[string]string{
		"double":        `persist(new KVStore(), { id: "petsByOwner" })`,
		"single":        `persist(new KVStore(), { id: 'petsByOwner' })`,
		"template":      "persist(new KVStore(), { id: `petsByOwner` })",
		"concatenated":  `persist(new KVStore(), { id: "pets" + "ByOwner" })`,
		"parenthesised": `persist(new KVStore(), { id: ("pets" + "ByOwner") })`,
		"multiline": `persist(new KVStore(), {
    id:
      "pets" +
      "ByOwner",
  })`,
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			hints, d := detect(t, "app.js",
				"import { persist, KVStore } from \"@cloudcompiler/sdk\";\nconst pets = "+call+";\n")
			if d.HasErrors() {
				t.Fatalf("diagnostics: %v", d.Items())
			}
			if len(hints) != 1 || hints[0].ID() != "petsByOwner" {
				t.Fatalf("hints = %+v", hints)
			}
		})
	}
}

// A template literal with a substitution is genuinely not knowable without
// running the program, and must stay an error rather than be guessed at.
func TestSubstitutedTemplateIsRejected(t *testing.T) {
	_, d := detect(t, "app.js", `
import { persist, KVStore } from "@cloudcompiler/sdk";
const env = "prod";
const pets = persist(new KVStore(), { id: `+"`pets-${env}`"+` });
`)
	items := d.Items()
	if len(items) != 1 || !strings.Contains(items[0].Message, "template literal") {
		t.Fatalf("diagnostics = %v", items)
	}
}

func TestNonLiteralIsAPreciseError(t *testing.T) {
	_, d := detect(t, "app.js", `
import { persist, KVStore } from "@cloudcompiler/sdk";
const name = "petsByOwner";
const pets = persist(new KVStore(), { id: name });
`)
	items := d.Items()
	if len(items) != 1 {
		t.Fatalf("diagnostics = %v", items)
	}
	got := items[0].String()
	if !strings.HasPrefix(got, "app.js:4:43: error: id must be") {
		t.Errorf("the error should point at the argument: %q", got)
	}
	if !strings.Contains(got, "the variable name") {
		t.Errorf("the error should name the offending argument: %q", got)
	}
}

// A shorthand property refers to a variable, so it is not knowable either.
func TestShorthandPropertyIsRejected(t *testing.T) {
	_, d := detect(t, "app.js", `
import { executionUnit } from "@cloudcompiler/sdk";
const id = "api";
executionUnit({ id });
`)
	if !d.HasErrors() {
		t.Fatal("a shorthand property refers to a variable and cannot be read statically")
	}
}

// tree-sitter counts a comment as a named child in the JavaScript grammar too.
func TestCommentsBetweenArguments(t *testing.T) {
	hints, d := detect(t, "app.ts", `
import { configValue } from "@cloudcompiler/sdk";

const level = configValue(
  // which setting
  "log_level",
  {
    // what it falls back to
    default: "info",
    // and whether to encrypt it
    secret: false,
  },
);
`)
	if d.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", d.Items())
	}
	if len(hints) != 1 || hints[0].ID() != "log_level" || hints[0].Str("default") != "info" {
		t.Fatalf("hints = %+v", hints)
	}
}

func TestUnknownOptionIsRejected(t *testing.T) {
	_, d := detect(t, "app.js", `
import { persist, KVStore } from "@cloudcompiler/sdk";
const pets = persist(new KVStore(), { id: "a", nope: 1 });
`)
	items := d.Items()
	if len(items) != 1 || !strings.Contains(items[0].Message, "not an option") {
		t.Fatalf("diagnostics = %v", items)
	}
}

func TestUnrelatedCallsIgnored(t *testing.T) {
	hints, d := detect(t, "app.js", `
import { persist, KVStore } from "@cloudcompiler/sdk";
import other from "other";
other.persist(new other.KVStore(), { id: "not-ours" });
console.log("hello");
const pets = persist(new KVStore(), { id: "ours" });
`)
	if d.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", d.Items())
	}
	if len(hints) != 1 || hints[0].ID() != "ours" {
		t.Fatalf("hints = %+v", hints)
	}
}

func TestNoSDKImportMeansNoHints(t *testing.T) {
	hints, _ := detect(t, "app.js", `const pets = persist(new KVStore(), { id: "x" });`)
	if len(hints) != 0 {
		t.Fatalf("hints = %+v", hints)
	}
}

func TestTypeScriptAnnotationsDoNotHide(t *testing.T) {
	hints, d := detect(t, "app.ts", `
import { persist, KVStore } from "@cloudcompiler/sdk";

const pets: KVStore = persist(new KVStore(), { id: "petsByOwner" });

export function get(id: string): Promise<unknown> {
  return pets.get(id);
}
`)
	if d.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", d.Items())
	}
	if len(hints) != 1 || hints[0].ID() != "petsByOwner" {
		t.Fatalf("hints = %+v", hints)
	}
}

func TestEnclosingFunctionRecorded(t *testing.T) {
	hints, _ := detect(t, "app.js", `
import { persist, Topic } from "@cloudcompiler/sdk";
function handler() {
  const t = persist(new Topic(), { id: "events" });
  return t;
}
`)
	if len(hints) != 1 || hints[0].Enclosing != "handler" {
		t.Fatalf("hints = %+v", hints)
	}
}

func TestSpanCoversTheWholeCall(t *testing.T) {
	src := "import { persist, KVStore } from \"@cloudcompiler/sdk\";\nconst pets = persist(new KVStore(), { id: \"petsByOwner\" });\n"
	hints, _ := detect(t, "app.js", src)
	if got, want := src[hints[0].Span[0]:hints[0].Span[1]], `persist(new KVStore(), { id: "petsByOwner" })`; got != want {
		t.Errorf("span text = %q, want %q", got, want)
	}
}

func TestEverySDKFunctionIsMapped(t *testing.T) {
	// Every function the shared surface declares must have a JavaScript
	// spelling, or a capability would be silently unreachable from Node.
	for _, fn := range sdkdetect.FunctionNames() {
		if _, ok := jsName[fn]; !ok {
			t.Errorf("%s has no JavaScript spelling", fn)
		}
	}
	if len(sdkFunction) != len(sdkdetect.FunctionNames()) {
		t.Errorf("the JavaScript surface has %d functions, the shared one has %d",
			len(sdkFunction), len(sdkdetect.FunctionNames()))
	}
}

func TestRouteDiscovery(t *testing.T) {
	files := source.NewSet("/tmp")
	f := &source.File{Path: "server.js", Content: []byte(`
import express from "express";
import { expose } from "@cloudcompiler/sdk";
const app = express();
expose(app, { id: "gw" });

app.get("/health", (req, res) => res.json({}));
app.get("/pets/:petId", (req, res) => res.json({}));
app.put("/pets/:petId", (req, res) => res.json({}));
app.get("/v1/" + "items", (req, res) => res.json({}));
app.use("/mounted", other);
`)}
	if err := (Frontend{}).Parse(f); err != nil {
		t.Fatal(err)
	}
	files.Add(f)

	got := routes(files, []string{"server.js"}, "app")
	want := []string{"GET /health", "GET /pets/{petId}", "PUT /pets/{petId}", "GET /v1/items"}
	var names []string
	for _, r := range got {
		names = append(names, r.Verb+" "+r.Path)
	}
	// Sorted by path, so compare as a set.
	if len(names) != len(want) {
		t.Fatalf("routes = %v, want %v", names, want)
	}
	for _, w := range want {
		found := false
		for _, n := range names {
			if n == w {
				found = true
			}
		}
		if !found {
			t.Errorf("missing route %q in %v", w, names)
		}
	}
}

func TestNormalisePath(t *testing.T) {
	cases := map[string]string{
		"/pets/:petId":   "/pets/{petId}",
		"/a/:x/b/:y":     "/a/{x}/b/{y}",
		"/plain":         "/plain",
		"/optional/:id?": "/optional/{id}",
	}
	for in, want := range cases {
		if got := normalisePath(in); got != want {
			t.Errorf("normalisePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOwns(t *testing.T) {
	front := Frontend{}
	for _, yes := range []string{"a.js", "a.mjs", "a.cjs", "a.ts", "a.mts", "a.tsx"} {
		if !front.Owns(yes) {
			t.Errorf("%s should be claimed", yes)
		}
	}
	// A declaration file states types it does not implement; bundling one
	// would ship a module that shadows the real thing.
	for _, no := range []string{"a.d.ts", "a.py", "a.json", "a.md"} {
		if front.Owns(no) {
			t.Errorf("%s should not be claimed", no)
		}
	}
}

func TestClosureFollowsBothModuleSystems(t *testing.T) {
	files := source.NewSet("/tmp")
	add := func(path, src string) {
		f := &source.File{Path: path, Content: []byte(src)}
		if err := (Frontend{}).Parse(f); err != nil {
			t.Fatal(err)
		}
		files.Add(f)
	}
	add("server.js", `
import { helper } from "./lib/helper.js";
const legacy = require("./legacy");
export { thing } from "./re-exported.js";
import fastify from "fastify";
`)
	add("lib/helper.js", `import { deep } from "./deep.js";\nexport const helper = deep;`)
	add("lib/deep.js", `export const deep = 1;`)
	add("legacy.js", `module.exports = {};`)
	add("re-exported.js", `export const thing = 1;`)
	add("unreached.js", `export const nope = 1;`)

	got, unresolved := closure(files, "server.js", nil)
	want := []string{"legacy.js", "lib/deep.js", "lib/helper.js", "re-exported.js", "server.js"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("closure = %v, want %v", got, want)
	}
	if len(unresolved) != 0 {
		t.Errorf("unexpected unresolved imports: %v", unresolved)
	}
}

// A TypeScript program imports "./store.js" meaning "./store.ts": the specifier
// names the file that will exist after compilation.
func TestClosureResolvesJsSpecifierToTypeScript(t *testing.T) {
	files := source.NewSet("/tmp")
	for path, src := range map[string]string{
		"server.ts": `import { store } from "./store.js";`,
		"store.ts":  `export const store = 1;`,
	} {
		f := &source.File{Path: path, Content: []byte(src)}
		if err := (Frontend{}).Parse(f); err != nil {
			t.Fatal(err)
		}
		files.Add(f)
	}
	got, _ := closure(files, "server.ts", nil)
	if !reflect.DeepEqual(got, []string{"server.ts", "store.ts"}) {
		t.Errorf("closure = %v", got)
	}
}

// A package import is npm's business; only a relative one that resolves to
// nothing is worth reporting.
func TestUnresolvedRelativeImportIsReported(t *testing.T) {
	files := source.NewSet("/tmp")
	f := &source.File{Path: "server.js", Content: []byte(`
import { a } from "./missing.js";
import express from "express";
`)}
	if err := (Frontend{}).Parse(f); err != nil {
		t.Fatal(err)
	}
	files.Add(f)

	_, unresolved := closure(files, "server.js", nil)
	if len(unresolved) != 1 || unresolved[0].Rendered != "./missing.js" {
		t.Fatalf("unresolved = %+v", unresolved)
	}
}
