package fuzz

import (
	"fmt"
	"github.com/cloudcompiler/cloudcc/internal/config"
	"math/rand"
	"sort"
	"strings"
)

// This file is the Node half of the generator. It plants the same ground truth
// the Python half does -- the Expectations a correct compile must reproduce --
// so both languages are checked by one oracle.
//
// It is written beside the Python generator rather than sharing its program
// builder. The two languages differ in more than syntax (module systems,
// Express instead of FastAPI, no package __init__), and threading both through
// one builder would have meant touching the order in which the Python side
// draws from its rng. That order is what makes the twenty pinned corpus seeds
// reproduce, so it is not worth disturbing to save some structure here.

// defaultUnitID is the single unit a program gets when it declares none. It
// must agree with capabilities.DefaultUnitID, which this package cannot import
// without a cycle.
const defaultUnitID = "main"

// jsImportStyle is how a module refers to the SDK.
type jsImportStyle int

const (
	// jsNamed: import { persist, KVStore } from "@cloudcompiler/sdk"
	jsNamed jsImportStyle = iota
	// jsAliased: import { persist as store } from "@cloudcompiler/sdk"
	jsAliased
	// jsNamespace: import * as sdk from "@cloudcompiler/sdk"
	jsNamespace
	// jsRequire: const { persist } = require("@cloudcompiler/sdk")
	jsRequire
	// jsRequireNamespace: const sdk = require("@cloudcompiler/sdk")
	jsRequireNamespace
	// jsRequireRenamed: const { persist: store } = require("@cloudcompiler/sdk")
	jsRequireRenamed
)

var allJSImportStyles = []jsImportStyle{
	jsNamed, jsAliased, jsNamespace, jsRequire, jsRequireNamespace, jsRequireRenamed,
}

// esm reports whether a style uses import rather than require, which decides
// the file extension and how local modules are pulled in.
func (s jsImportStyle) esm() bool {
	return s == jsNamed || s == jsAliased || s == jsNamespace
}

// jsNamespaceAliases are plausible names for a namespace import.
var jsNamespaceAliases = []string{"sdk", "cloudcc", "cc", "infra"}

// jsModule accumulates one generated JavaScript or TypeScript file.
type jsModule struct {
	rng *rand.Rand

	style     jsImportStyle
	namespace string
	banner    bool
	// typescript renders the module as .ts, with the annotations a person
	// writing TypeScript would add.
	typescript bool

	// needed maps an SDK export to the local name this module calls it by.
	needed map[string]string
	// stdImports are non-SDK imports, rendered before the SDK ones.
	stdImports []string
	// localImports are imports of other generated modules.
	localImports []string

	body []string
}

// newJSModule picks a module's shape. In behavioural mode the program has to be
// runnable before it is compiled, which still rules out CommonJS: the harness
// serves the app through a dynamic import.
//
// TypeScript used to be ruled out too -- "node cannot import a .ts file without
// a loader" -- and that was the reason no generated TypeScript program was ever
// run or deployed. The harness has a loader now (tsx, see serve_node in
// tests/e2e/lib.sh), so the differential suite covers TypeScript as well.
func newJSModule(rng *rand.Rand, behavioural bool) *jsModule {
	m := &jsModule{rng: rng, needed: map[string]string{}}
	for {
		m.style = allJSImportStyles[rng.Intn(len(allJSImportStyles))]
		if !behavioural || m.style.esm() {
			break
		}
	}
	m.namespace = jsNamespaceAliases[rng.Intn(len(jsNamespaceAliases))]
	m.banner = rng.Intn(3) > 0
	// TypeScript only under ESM: a .ts file compiled as CommonJS is a
	// configuration people do reach for, but it is not what this is testing.
	m.typescript = m.style.esm() && rng.Intn(4) == 0
	return m
}

// ext is the file extension this module must be written with.
func (m *jsModule) ext() string {
	if m.typescript {
		return ".ts"
	}
	if m.style.esm() {
		return ".js"
	}
	return ".cjs"
}

// localName returns the name this module calls an SDK export by, registering
// whatever import makes it available.
func (m *jsModule) localName(export string) string {
	if name, ok := m.needed[export]; ok {
		return name
	}
	var name string
	switch m.style {
	case jsNamed, jsRequire:
		name = export
	case jsAliased, jsRequireRenamed:
		name = jsDirectAlias(export)
	case jsNamespace, jsRequireNamespace:
		name = m.namespace + "." + export
	}
	m.needed[export] = name
	return name
}

// jsDirectAlias is the local name an aliased import binds an export to.
func jsDirectAlias(export string) string {
	switch export {
	case "persist":
		return "store"
	case "configValue":
		return "setting"
	case "expose":
		return "publishApp"
	case "executionUnit":
		return "unit"
	case "staticUnit":
		return "site"
	case "embedAssets":
		return "assets"
	}
	return export
}

// quote renders a JavaScript string literal in one of the several ways a
// person might, including the forms that have broken the compiler before.
func (m *jsModule) quote(s string) string {
	switch m.rng.Intn(6) {
	case 0:
		return "'" + s + "'"
	case 1:
		// A template literal with no substitution is a constant, and the
		// compiler is expected to read it as one.
		if !strings.ContainsAny(s, "`${") {
			return "`" + s + "`"
		}
	case 2:
		// Concatenation, which is what a formatter produces from a long line.
		if len(s) > 3 {
			at := 1 + m.rng.Intn(len(s)-2)
			return `"` + s[:at] + `" + "` + s[at:] + `"`
		}
	case 3:
		if len(s) > 3 {
			at := 1 + m.rng.Intn(len(s)-2)
			return `("` + s[:at] + `" + "` + s[at:] + `")`
		}
	}
	return `"` + s + `"`
}

func (m *jsModule) line(s string)            { m.body = append(m.body, s) }
func (m *jsModule) linef(f string, a ...any) { m.body = append(m.body, fmt.Sprintf(f, a...)) }
func (m *jsModule) blank()                   { m.body = append(m.body, "") }

func (m *jsModule) importStd(spec string) {
	for _, existing := range m.stdImports {
		if existing == spec {
			return
		}
	}
	m.stdImports = append(m.stdImports, spec)
}

func (m *jsModule) importLocal(spec string) {
	for _, existing := range m.localImports {
		if existing == spec {
			return
		}
	}
	m.localImports = append(m.localImports, spec)
}

// declare binds a value, varying the keyword and occasionally annotating it.
func (m *jsModule) declare(name, value string) {
	keyword := "const"
	if m.rng.Intn(6) == 0 {
		keyword = "let"
	}
	if m.typescript && m.rng.Intn(3) == 0 {
		m.linef("%s %s: unknown = %s;", keyword, name, value)
		return
	}
	m.linef("%s %s = %s;", keyword, name, value)
}

// call renders an SDK call, occasionally with a comment inside the argument
// list. tree-sitter counts a comment as a named node, so an argument reader
// that does not skip them treats one as an argument -- that was a hard compile
// error on ordinary Python code once, and the Node grammar behaves the same.
func (m *jsModule) call(export string, args ...string) string {
	callee := m.localName(export)
	if len(args) > 0 && m.rng.Intn(5) == 0 {
		return fmt.Sprintf("%s(\n  // the id is read at compile time\n  %s,\n)",
			callee, strings.Join(args, ",\n  "))
	}
	if m.rng.Intn(6) == 0 {
		return fmt.Sprintf("%s( %s )", callee, strings.Join(args, ", "))
	}
	return fmt.Sprintf("%s(%s)", callee, strings.Join(args, ", "))
}

// options renders an options object from already-rendered key/value pairs.
func (m *jsModule) options(pairs ...string) string {
	if len(pairs) == 0 {
		return "{}"
	}
	if m.rng.Intn(5) == 0 {
		return "{\n  " + strings.Join(pairs, ",\n  ") + ",\n}"
	}
	return "{ " + strings.Join(pairs, ", ") + " }"
}

// clientExpr renders the client a persist() call wraps, registering whatever
// import it needs.
//
// This is the most valuable surface in the file. The compiler reads the type of
// this expression to decide what to provision and which client library to hand
// back, so every shape it can take is a distinct path through the detector.
func (m *jsModule) clientExpr(rng *rand.Rand, kind string) string {
	switch kind {
	case "persist_kv":
		// A key/value store is the AWS SDK's own client. There is no cloudcc
		// class for it: the compiled program gets a DynamoDBClient too, with
		// every command it sends bound to the provisioned table.
		m.importStd(m.importFrom("{ DynamoDBClient }", "@aws-sdk/client-dynamodb"))
		if rng.Intn(2) == 0 {
			return "new DynamoDBClient({})"
		}
		return "new DynamoDBClient(" + m.options("region: "+m.quote("us-east-1")) + ")"
	case "persist_secret":
		return "new " + m.localName("Secret") + "()"
	case "pubsub":
		return "new " + m.localName("Topic") + "()"
	case "persist_fs":
		m.importStd(m.importFrom("{ S3Client }", "@aws-sdk/client-s3"))
		return "new S3Client({})"

	case "persist_orm":
		url := []string{
			"postgresql://localhost/app",
			"postgres://localhost/app",
			"mysql://localhost/app",
		}[rng.Intn(3)]
		if rng.Intn(2) == 0 {
			m.importStd(m.importFrom("{ Pool }", "pg"))
			return "new Pool(" + m.options("connectionString: "+m.quote(url)) + ")"
		}
		m.importStd(m.importFrom("knex", "knex"))
		return "knex(" + m.options("client: "+m.quote("pg"), "connection: "+m.quote(url)) + ")"

	case "persist_redis":
		if rng.Intn(2) == 0 {
			m.importStd(m.importFrom("Redis", "ioredis"))
			return "new Redis()"
		}
		m.importStd(m.importFrom("{ createClient }", "redis"))
		return "createClient()"
	}
	m.importStd(m.importFrom("{ DynamoDBClient }", "@aws-sdk/client-dynamodb"))
	return "new DynamoDBClient({})"
}

// importFrom renders a third-party import in this module's module system.
func (m *jsModule) importFrom(binding, pkg string) string {
	if m.style.esm() {
		return fmt.Sprintf("import %s from %q;", binding, pkg)
	}
	// require has no default-import sugar; a bare binding destructures nothing.
	if strings.HasPrefix(binding, "{") {
		return fmt.Sprintf("const %s = require(%q);", binding, pkg)
	}
	return fmt.Sprintf("const %s = require(%q);", binding, pkg)
}

// render assembles the module: banner, third-party imports, the SDK import,
// local imports, then the body.
func (m *jsModule) render() string {
	var b strings.Builder
	if m.banner {
		b.WriteString("// Generated by cloudcc's program generator.\n\n")
	}
	for _, spec := range m.stdImports {
		b.WriteString(spec + "\n")
	}
	if len(m.stdImports) > 0 {
		b.WriteString("\n")
	}
	if spec := m.sdkImportSpec(); spec != "" {
		b.WriteString(spec + "\n\n")
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

// sdkImportSpec renders the import that binds every SDK export this module used.
func (m *jsModule) sdkImportSpec() string {
	if len(m.needed) == 0 {
		return ""
	}
	const pkg = "@cloudcompiler/sdk"

	switch m.style {
	case jsNamespace:
		return fmt.Sprintf("import * as %s from %q;", m.namespace, pkg)
	case jsRequireNamespace:
		return fmt.Sprintf("const %s = require(%q);", m.namespace, pkg)
	}

	exports := sortedKeys(m.needed)
	parts := make([]string, 0, len(exports))
	for _, export := range exports {
		local := m.needed[export]
		switch m.style {
		case jsNamed, jsRequire:
			parts = append(parts, export)
		case jsAliased:
			parts = append(parts, export+" as "+local)
		case jsRequireRenamed:
			parts = append(parts, export+": "+local)
		}
	}
	joined := strings.Join(parts, ", ")
	if m.style.esm() {
		return fmt.Sprintf("import { %s } from %q;", joined, pkg)
	}
	return fmt.Sprintf("const { %s } = require(%q);", joined, pkg)
}

// nodeGenerator builds one Node program.
type nodeGenerator struct {
	rng  *rand.Rand
	opts Options
	// dir is the subdirectory modules live in, empty for a flat layout.
	dir string
}

// GenerateNode returns a deterministic Node program plus the truth about it.
// The same seed always produces the same program, byte for byte.
func GenerateNode(seed int64, opts Options) *Program {
	g := &nodeGenerator{rng: rand.New(rand.NewSource(seed)), opts: opts}
	return g.build(seed)
}

func (g *nodeGenerator) build(seed int64) *Program {
	if g.rng.Intn(2) == 0 {
		g.dir = "src"
	}

	p := &Program{
		Seed:  seed,
		Name:  fmt.Sprintf("gen-node-%d", seed),
		Files: map[string]string{},
		Expect: Expectations{
			Gateways:      map[string]GatewayExpect{},
			Stores:        map[string]string{},
			ConfigVars:    map[string]ConfigExpect{},
			EntryFile:     map[string]string{},
			EmbeddedFiles: map[string][]string{},
			ComputeTypes:  map[string]string{},
		},
	}

	multi := !g.opts.Behavioural && g.rng.Intn(3) == 0
	unitCount := 1
	if multi {
		unitCount = 2
	}

	// ------------------------------------------------------- shared stores
	// Every unit imports this module, which is what makes two units resolve to
	// one table rather than two.
	storeMod := newJSModule(g.rng, g.opts.Behavioural)
	storeNames := map[string]string{}
	kinds := g.pickKinds()

	for i, kind := range kinds {
		id := makeID(g.rng, storeBase(kind), i)
		v := jsIdentifier(id)
		storeNames[id] = v
		storeMod.declare(v, storeMod.call("persist",
			storeMod.clientExpr(g.rng, kind),
			storeMod.options("id: "+storeMod.quote(id))))
		p.Expect.Stores[id] = kind
	}

	topicID := ""
	if multi && g.rng.Intn(2) == 0 {
		topicID = makeID(g.rng, "events", g.rng.Intn(9))
		storeMod.declare("events", storeMod.call("persist",
			storeMod.clientExpr(g.rng, KindPubSub),
			storeMod.options("id: "+storeMod.quote(topicID))))
		p.Expect.Topics = append(p.Expect.Topics, topicID)
	}

	primaryStore := g.pickKVStore(storeNames, p.Expect.Stores)
	p.PrimaryStore = primaryStore

	storeMod.blank()
	storeMod.line("export " + jsExportList(storeNames, topicID) + ";")
	if !storeMod.style.esm() {
		// CommonJS says it differently, and both spellings are idiomatic.
		storeMod.body[len(storeMod.body)-1] = "module.exports = " +
			jsObjectLiteral(storeNames, topicID) + ";"
	}

	storeRel := g.path("store" + storeMod.ext())
	p.Files[storeRel] = storeMod.render()
	p.Expect.SharedModules = append(p.Expect.SharedModules, storeRel)

	// -------------------------------------------------------- config values
	cfgVars := map[string]ConfigExpect{}
	for i := 0; i < g.rng.Intn(3); i++ {
		id := makeID(g.rng, "setting", i)
		expect := ConfigExpect{Default: fmt.Sprintf("value-%d", i)}
		if g.rng.Intn(4) == 0 {
			expect.Secret = true
		}
		cfgVars[id] = expect
		p.Expect.ConfigVars[id] = expect
	}

	// --------------------------------------------------------------- units
	//
	// A program with no executionUnit call at all is compiled as a single unit
	// named "main". Leaving the call out half the time is worth doing: it is
	// the smallest program anyone writes, and the name it gets is a decision
	// the compiler makes rather than one the source states.
	declare := multi || g.rng.Intn(2) == 0

	units := make([]string, 0, unitCount)
	if !declare {
		units = append(units, defaultUnitID)
	} else {
		for i := 0; i < unitCount; i++ {
			units = append(units, unitBase(i))
		}
	}
	sort.Strings(units)
	p.Expect.Units = units
	p.PrimaryUnit = units[0]

	for i, unitID := range units {
		g.writeUnit(p, unitID, i == 0, declare, storeRel, storeNames,
			primaryStore, topicID, cfgVars)
	}
	for _, u := range units {
		p.Expect.ComputeTypes[u] = config.TypeFunction
	}

	p.Config = g.renderConfig(p)
	p.Files["package.json"] = g.renderPackageJSON(p)
	p.Scenario = g.scenario(primaryStore, p.RoutePrefix)
	return p
}

// pickKinds chooses which store kinds the program declares.
func (g *nodeGenerator) pickKinds() []string {
	pool := []string{KindPersistKV, KindPersistFS, KindPersistSecret,
		KindPersistORM, KindPersistRedis}
	if g.opts.Behavioural {
		// The differential harness compares observable behaviour, so it is
		// limited to the stores whose emulated and real forms agree.
		pool = []string{KindPersistKV, KindPersistFS}
	}
	kinds := []string{KindPersistKV}
	for i := 0; i < g.rng.Intn(3); i++ {
		kinds = append(kinds, pool[g.rng.Intn(len(pool))])
	}
	return kinds
}

// pickKVStore returns the variable of a KV store, which is what the HTTP
// surface is built on.
func (g *nodeGenerator) pickKVStore(names map[string]string, kinds map[string]string) string {
	for _, id := range sortedKeys(names) {
		if kinds[id] == KindPersistKV {
			return id
		}
	}
	return sortedKeys(names)[0]
}

// writeUnit writes one execution unit's entry module.
func (g *nodeGenerator) writeUnit(p *Program, unitID string, exposed, declare bool,
	storeRel string, storeNames map[string]string, primaryStore, topicID string,
	cfgVars map[string]ConfigExpect) {

	m := newJSModule(g.rng, g.opts.Behavioural)
	// A unit importing a CommonJS module has to be CommonJS itself here: the
	// generator does not mix module systems within one program, which is a
	// real thing people do but not what this is testing.
	if strings.HasSuffix(storeRel, ".cjs") && m.style.esm() {
		m.style = jsRequire
		m.typescript = false
	} else if !strings.HasSuffix(storeRel, ".cjs") && !m.style.esm() {
		m.style = jsNamed
	}

	names := storeImportNames(storeNames, topicID)
	m.importLocal(g.localImportSpec(m, names, storeRel))

	if declare {
		m.line(m.call("executionUnit", m.options("id: "+m.quote(unitID))) + ";")
		m.blank()
	}

	for _, id := range sortedKeys(cfgVars) {
		expect := cfgVars[id]
		opts := []string{"default: " + m.quote(expect.Default)}
		if expect.Secret {
			opts = append(opts, "secret: true")
		}
		m.declare(jsIdentifier(id), m.call("configValue", m.quote(id), m.options(opts...)))
	}
	if len(cfgVars) > 0 {
		m.blank()
	}

	if exposed {
		g.writeExposedBody(p, m, unitID, storeNames, primaryStore, topicID)
	} else {
		g.writeWorkerBody(m, storeNames, primaryStore, topicID)
	}

	rel := g.path(unitID + m.ext())
	p.Files[rel] = m.render()
	p.Expect.EntryFile[unitID] = rel
	if exposed {
		p.EntryModule = rel
	}
}

// writeExposedBody writes an Express application and the routes the compiler is
// expected to discover.
func (g *nodeGenerator) writeExposedBody(p *Program, m *jsModule, unitID string,
	storeNames map[string]string, primaryStore, topicID string) {

	m.importStd(m.importFrom("express", "express"))
	appVar := []string{"app", "api", "server"}[g.rng.Intn(3)]
	m.declare(appVar, "express()")
	m.linef("%s.use(express.json());", appVar)
	p.AppVar = appVar
	m.blank()

	gatewayID := makeID(g.rng, "gw", g.rng.Intn(9))
	exposeArgs := []string{appVar}
	if g.rng.Intn(3) > 0 {
		exposeArgs = append(exposeArgs, m.options("id: "+m.quote(gatewayID)))
	} else {
		gatewayID = "main" // the SDK's default when no id is given
	}
	m.line(m.call("expose", exposeArgs...) + ";")
	m.blank()

	store := storeNames[primaryStore]
	// The store is the AWS SDK's own client, so the commands and the item
	// shape are the program's. A JSON string keeps numbers as numbers, which a
	// native DynamoDB attribute would not.
	m.importStd(m.importFrom(
		"{ DeleteItemCommand, GetItemCommand, PutItemCommand, ScanCommand }",
		"@aws-sdk/client-dynamodb"))
	base := routeSet(g.rng)
	p.RoutePrefix = base
	var routes []Route

	m.linef("%s.get(%s, async (req, res) => {", appVar, m.quote("/health"))
	m.line(`  res.json({ status: "ok" });`)
	m.line("});")
	routes = append(routes, Route{"GET", "/health"})
	m.blank()

	m.linef("%s.get(%s, async (req, res) => {", appVar, m.quote(base+"/:itemId"))
	m.linef("  const found = await %s.send(new GetItemCommand({", store)
	m.line(`    TableName: "items",`)
	m.line("    Key: { id: { S: req.params.itemId } },")
	m.line("  }));")
	m.line("  if (!found.Item) {")
	m.line(`    res.status(404).json({ detail: "missing" });`)
	m.line("    return;")
	m.line("  }")
	m.line("  res.json(JSON.parse(found.Item.value.S));")
	m.line("});")
	routes = append(routes, Route{"GET", base + "/{itemId}"})
	m.blank()

	m.linef("%s.put(%s, async (req, res) => {", appVar, m.quote(base+"/:itemId"))
	m.linef("  await %s.send(new PutItemCommand({", store)
	m.line(`    TableName: "items",`)
	m.line("    Item: { id: { S: req.params.itemId }, value: { S: JSON.stringify(req.body) } },")
	m.line("  }));")
	if topicID != "" {
		m.line("  await events.publish({ id: req.params.itemId });")
	}
	m.line("  res.json({ ok: true, id: req.params.itemId });")
	m.line("});")
	routes = append(routes, Route{"PUT", base + "/{itemId}"})
	m.blank()

	m.linef("%s.delete(%s, async (req, res) => {", appVar, m.quote(base+"/:itemId"))
	m.linef("  await %s.send(new DeleteItemCommand({", store)
	m.line(`    TableName: "items",`)
	m.line("    Key: { id: { S: req.params.itemId } },")
	m.line("  }));")
	m.line("  res.json({ ok: true });")
	m.line("});")
	routes = append(routes, Route{"DELETE", base + "/{itemId}"})
	m.blank()

	m.linef("%s.get(%s, async (req, res) => {", appVar, m.quote(base))
	m.linef("  const page = await %s.send(new ScanCommand({", store)
	m.line(`    TableName: "items",`)
	m.line(`    ProjectionExpression: "id",`)
	m.line("  }));")
	m.line("  res.json({ keys: (page.Items ?? []).map((item) => item.id.S).sort() });")
	m.line("});")
	routes = append(routes, Route{"GET", base})
	m.blank()

	if m.style.esm() {
		m.linef("export { %s };", appVar)
	} else {
		m.linef("module.exports = { %s };", appVar)
	}

	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Path != routes[j].Path {
			return routes[i].Path < routes[j].Path
		}
		return routes[i].Verb < routes[j].Verb
	})
	p.Expect.Gateways[gatewayID] = GatewayExpect{Unit: unitID, Routes: routes}
}

// writeWorkerBody writes a unit with no HTTP surface.
func (g *nodeGenerator) writeWorkerBody(m *jsModule, storeNames map[string]string,
	primaryStore, topicID string) {

	store := storeNames[primaryStore]
	m.importStd(m.importFrom("{ ScanCommand }", "@aws-sdk/client-dynamodb"))
	count := fmt.Sprintf(
		"  const page = await %s.send(new ScanCommand({ TableName: \"items\", ProjectionExpression: \"id\" }));\n"+
			"  return (page.Items ?? []).length;", store)
	m.blank()
	m.line("export async function summarise() {")
	m.line(count)
	m.line("}")
	if !m.style.esm() {
		m.body = m.body[:len(m.body)-3]
		m.line("async function summarise() {")
		m.line(count)
		m.line("}")
		m.line("module.exports = { summarise };")
	}

	if topicID != "" {
		m.blank()
		m.line("events.subscribe((message) => ({ seen: message.id }));")
	}
}

// localImportSpec renders the import of the shared store module.
func (g *nodeGenerator) localImportSpec(m *jsModule, names []string, storeRel string) string {
	spec := "./" + strings.TrimSuffix(lastSegment(storeRel), ".ts")
	if m.style.esm() {
		// ESM needs the extension, and a .ts source is imported as .js.
		if strings.HasSuffix(storeRel, ".ts") {
			spec += ".js"
		}
		return fmt.Sprintf("import { %s } from %q;", strings.Join(names, ", "), spec)
	}
	return fmt.Sprintf("const { %s } = require(%q);", strings.Join(names, ", "), spec)
}

// path places a file according to the chosen layout.
func (g *nodeGenerator) path(name string) string {
	if g.dir == "" {
		return name
	}
	return g.dir + "/" + name
}

// renderPackageJSON writes the manifest the compiler reads to learn the unit's
// module system and dependencies.
func (g *nodeGenerator) renderPackageJSON(p *Program) string {
	typeField := "\"type\": \"module\",\n  "
	if strings.HasSuffix(p.EntryModule, ".cjs") {
		typeField = ""
	}
	return fmt.Sprintf(`{
  "name": %q,
  "private": true,
  %s"dependencies": {
    "express": "^4.21.2"
  }
}
`, p.Name, typeField)
}

// renderConfig writes the cloudcc.yaml the program compiles against.
func (g *nodeGenerator) renderConfig(p *Program) string {
	var b strings.Builder
	fmt.Fprintf(&b, "app: %s\n", p.Name)
	b.WriteString("provider: aws\n")
	return b.String()
}

// scenario is the request sequence the differential harness replays.
func (g *nodeGenerator) scenario(store, base string) []Step {
	return []Step{
		{Method: "GET", Path: "/health"},
		{Method: "PUT", Path: base + "/a", Body: `{"name":"first"}`, Store: store},
		{Method: "GET", Path: base + "/a", Store: store},
		{Method: "PUT", Path: base + "/b", Body: `{"name":"second"}`, Store: store},
		{Method: "GET", Path: base, Store: store},
		{Method: "DELETE", Path: base + "/a", Store: store},
		{Method: "GET", Path: base + "/a", Store: store},
		{Method: "GET", Path: base, Store: store},
	}
}

// jsIdentifier turns an id into a valid JavaScript binding.
func jsIdentifier(id string) string {
	var b strings.Builder
	upper := false
	for i, r := range id {
		switch {
		case r == '-' || r == '.' || r == '_':
			upper = true
		case upper:
			b.WriteString(strings.ToUpper(string(r)))
			upper = false
		default:
			if i == 0 && r >= '0' && r <= '9' {
				b.WriteString("v")
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}

// jsExportList renders `{ a, b }` for an ESM export statement.
func jsExportList(storeNames map[string]string, topicID string) string {
	return "{ " + strings.Join(storeImportNames(storeNames, topicID), ", ") + " }"
}

// jsObjectLiteral renders the same set for module.exports.
func jsObjectLiteral(storeNames map[string]string, topicID string) string {
	return "{ " + strings.Join(storeImportNames(storeNames, topicID), ", ") + " }"
}

// lastSegment returns the file name of a slash-separated path.
func lastSegment(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}
