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
import { persist, Topic } from "@cloudcompiler/sdk";
const pets = persist(new DynamoDBClient({}), { id: "petsByOwner" });
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
import { persist as store, Topic } from "@cloudcompiler/sdk";
const pets = store(new DynamoDBClient({}), { id: "a" });
`)
	if len(hints) != 1 || hints[0].ID() != "a" {
		t.Fatalf("hints = %+v", hints)
	}
}

func TestDetectNamespaceImport(t *testing.T) {
	hints, _ := detect(t, "app.js", `
import * as sdk from "@cloudcompiler/sdk";
const pets = sdk.persist(new sdk.Topic(), { id: "a" });
`)
	if len(hints) != 1 || hints[0].Func != sdkdetect.FnPersist {
		t.Fatalf("hints = %+v", hints)
	}
}

// Both module systems are idiomatic and a program may mix them.
func TestDetectRequireForms(t *testing.T) {
	cases := map[string]string{
		"destructured": `const { persist, Topic } = require("@cloudcompiler/sdk");
const pets = persist(new DynamoDBClient({}), { id: "a" });`,
		"namespace": `const sdk = require("@cloudcompiler/sdk");
const pets = sdk.persist(new sdk.Topic(), { id: "a" });`,
		"renamed": `const { persist: kv, Topic } = require("@cloudcompiler/sdk");
const pets = kv(new DynamoDBClient({}), { id: "a" });`,
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
		"double":        `persist(new DynamoDBClient({}), { id: "petsByOwner" })`,
		"single":        `persist(new DynamoDBClient({}), { id: 'petsByOwner' })`,
		"template":      "persist(new DynamoDBClient({}), { id: `petsByOwner` })",
		"concatenated":  `persist(new DynamoDBClient({}), { id: "pets" + "ByOwner" })`,
		"parenthesised": `persist(new DynamoDBClient({}), { id: ("pets" + "ByOwner") })`,
		"multiline": `persist(new DynamoDBClient({}), {
    id:
      "pets" +
      "ByOwner",
  })`,
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			hints, d := detect(t, "app.js",
				"import { DynamoDBClient } from \"@aws-sdk/client-dynamodb\";\nimport { persist } from \"@cloudcompiler/sdk\";\nconst pets = "+call+";\n")
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
import { persist, Topic } from "@cloudcompiler/sdk";
const env = "prod";
const pets = persist(new DynamoDBClient({}), { id: `+"`pets-${env}`"+` });
`)
	items := d.Items()
	if len(items) != 1 || !strings.Contains(items[0].Message, "template literal") {
		t.Fatalf("diagnostics = %v", items)
	}
}

func TestNonLiteralIsAPreciseError(t *testing.T) {
	_, d := detect(t, "app.js", `
import { persist, Topic } from "@cloudcompiler/sdk";
const name = "petsByOwner";
const pets = persist(new DynamoDBClient({}), { id: name });
`)
	items := d.Items()
	if len(items) != 1 {
		t.Fatalf("diagnostics = %v", items)
	}
	got := items[0].String()
	if !strings.HasPrefix(got, "app.js:4:52: error: id must be") {
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
import { persist, Topic } from "@cloudcompiler/sdk";
const pets = persist(new DynamoDBClient({}), { id: "a", nope: 1 });
`)
	items := d.Items()
	if len(items) != 1 || !strings.Contains(items[0].Message, "not an option") {
		t.Fatalf("diagnostics = %v", items)
	}
}

func TestUnrelatedCallsIgnored(t *testing.T) {
	hints, d := detect(t, "app.js", `
import { persist, Topic } from "@cloudcompiler/sdk";
import other from "other";
other.persist(new other.Topic(), { id: "not-ours" });
console.log("hello");
const pets = persist(new DynamoDBClient({}), { id: "ours" });
`)
	if d.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", d.Items())
	}
	if len(hints) != 1 || hints[0].ID() != "ours" {
		t.Fatalf("hints = %+v", hints)
	}
}

func TestNoSDKImportMeansNoHints(t *testing.T) {
	hints, _ := detect(t, "app.js", `const pets = persist(new DynamoDBClient({}), { id: "x" });`)
	if len(hints) != 0 {
		t.Fatalf("hints = %+v", hints)
	}
}

func TestTypeScriptAnnotationsDoNotHide(t *testing.T) {
	hints, d := detect(t, "app.ts", `
import { persist, Topic } from "@cloudcompiler/sdk";

const pets: Topic = persist(new DynamoDBClient({}), { id: "petsByOwner" });

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
	src := "import { DynamoDBClient } from \"@aws-sdk/client-dynamodb\";\nimport { persist } from \"@cloudcompiler/sdk\";\nconst pets = persist(new DynamoDBClient({}), { id: \"petsByOwner\" });\n"
	hints, _ := detect(t, "app.js", src)
	if got, want := src[hints[0].Span[0]:hints[0].Span[1]], `persist(new DynamoDBClient({}), { id: "petsByOwner" })`; got != want {
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

// A topic's arguments are its requirements, and the compiler chooses the
// backing service from them -- so they have to survive detection. JavaScript
// spells them camelCase and the IR uses the Python spelling, which is a
// translation that belongs at the language seam rather than downstream.
func TestTopicRequirementsAreReadAndTranslated(t *testing.T) {
	hints, d := detect(t, "app.js", `
import { Topic, persist } from "@cloudcompiler/sdk";
const audit = persist(new Topic({ replay: true, ordering: "key", maxMessageKb: 512 }), { id: "audit" });
`)
	if d.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", d.Items())
	}
	if len(hints) != 1 {
		t.Fatalf("got %d hints, want 1", len(hints))
	}
	got := hints[0].ClientArgs
	for key, want := range map[string]any{
		"replay":         true,
		"ordering":       "key",
		"max_message_kb": 512,
	} {
		if got[key] != want {
			t.Errorf("ClientArgs[%q] = %#v, want %#v", key, got[key], want)
		}
	}
}

// The arguments of a library's own client are none of the compiler's business:
// the region on a DynamoDBClient is talking to the local emulator, and reading
// it as a declaration would be a category error.
func TestALibraryClientsArgumentsAreNotRead(t *testing.T) {
	hints, d := detect(t, "app.js", `
import { DynamoDBClient } from "@aws-sdk/client-dynamodb";
import { persist } from "@cloudcompiler/sdk";
const pets = persist(new DynamoDBClient({ region: "eu-west-1" }), { id: "pets" });
`)
	if d.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", d.Items())
	}
	if hints[0].ClientArgs != nil {
		t.Errorf("ClientArgs = %v, want nothing read", hints[0].ClientArgs)
	}
}

// TypeScript writes the same values with type syntax around them. None of it
// survives to runtime, and all of it used to stop the compiler recognising
// what it was looking at: the node under inspection was an as_expression, and
// the new_expression was inside it.
func TestTypeScriptWrappersAroundAClientAreErased(t *testing.T) {
	for name, expr := range map[string]string{
		"as":           `new Redis() as Redis`,
		"satisfies":    `new Redis() satisfies Redis`,
		"assertion":    `<Redis>new Redis()`,
		"parenthesers": `(new Redis())`,
		"stacked":      `((new Redis() as Redis)!)`,
	} {
		t.Run(name, func(t *testing.T) {
			hints, d := detect(t, "app.ts", `
import Redis from "ioredis";
import { persist } from "@cloudcompiler/sdk";
const cache = persist(`+expr+`, { id: "itemCache" });
`)
			if d.HasErrors() {
				t.Fatalf("unexpected diagnostics: %v", d.Items())
			}
			if len(hints) != 1 {
				t.Fatalf("hints = %+v", hints)
			}
			if got := hints[0].ClientLibrary; got != sdkdetect.LibIORedis {
				t.Errorf("library = %q, want %q -- the wrapper hid the constructor",
					got, sdkdetect.LibIORedis)
			}
		})
	}
}

// A variable is rejected in either language: persist() reads the client's type
// from the expression, and a name does not carry one. What this pins is that
// the TypeScript spelling gets the *same* answer -- the message names `pool`,
// not `pool!`, so it reads as the rule it is rather than as a syntax the
// compiler failed to parse.
func TestANonNullAssertionOnAVariableIsStillJustAVariable(t *testing.T) {
	_, d := detect(t, "app.ts", `
import { Pool } from "pg";
import { persist } from "@cloudcompiler/sdk";
const pool = new Pool();
const db = persist(pool!, { id: "shopdb" });
`)
	if !d.HasErrors() {
		t.Fatal("a variable carries no type, so persist() cannot read a capability from it")
	}
	msg := d.Items()[0].Message
	if !strings.Contains(msg, "the variable pool") || strings.Contains(msg, "pool!") {
		t.Errorf("the message should name the binding, not its assertion:\n%s", msg)
	}
}

// An id narrowed with `as const` is still a literal, and an options object
// written `{...} as const` is still an options object -- read as a positional
// argument instead, it produced "persist() takes at most 1 positional
// argument".
func TestAsConstIsStillALiteral(t *testing.T) {
	for name, src := range map[string]string{
		"on the id":     `persist(new DynamoDBClient({}), { id: "pets" as const });`,
		"on the object": `persist(new DynamoDBClient({}), { id: "pets" } as const);`,
	} {
		t.Run(name, func(t *testing.T) {
			hints, d := detect(t, "app.ts", `
import { DynamoDBClient } from "@aws-sdk/client-dynamodb";
import { persist } from "@cloudcompiler/sdk";
const pets = `+src+`
`)
			if d.HasErrors() {
				t.Fatalf("unexpected diagnostics: %v", d.Items())
			}
			if len(hints) != 1 || hints[0].ID() != "pets" {
				t.Fatalf("hints = %+v", hints)
			}
		})
	}
}

// The application handed to expose() is named by a binding, and the rest of
// the compiler looks that binding up to find the routes declared on it. A
// wrapper left in place makes the name "app!", which matches nothing -- the
// gateway is created and the topology shows no routes at all.
func TestATypeScriptWrapperOnTheExposedAppIsErased(t *testing.T) {
	hints, d := detect(t, "app.ts", `
import express from "express";
import { expose } from "@cloudcompiler/sdk";
const app = express();
expose(app!, { id: "api" });
`)
	if d.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", d.Items())
	}
	if len(hints) != 1 {
		t.Fatalf("hints = %+v", hints)
	}
	if got := hints[0].Str("app"); got != "app" {
		t.Errorf("app = %q, want %q", got, "app")
	}
}

// Route paths go through the same literal decoder as hint arguments, which is
// the property that makes fixing one fix the other. This is that claim as a
// test: a route path narrowed with `as const` is read, and nothing in
// routes.go knows what `as const` is.
func TestRoutePathsAcceptTypeScriptNarrowing(t *testing.T) {
	files := source.NewSet("/tmp")
	f := &source.File{Path: "server.ts", Content: []byte(`
import express from "express";
import { expose } from "@cloudcompiler/sdk";
const app = express();
expose(app, { id: "gw" });

app.get("/health" as const, (req, res) => res.json({}));
app.get(("/pets/:petId") as const, (req, res) => res.json({}));
`)}
	if err := (Frontend{}).Parse(f); err != nil {
		t.Fatal(err)
	}
	files.Add(f)

	got := routes(files, []string{"server.ts"}, "app")
	var names []string
	for _, r := range got {
		names = append(names, r.Verb+" "+r.Path)
	}
	want := []string{"GET /health", "GET /pets/{petId}"}
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
