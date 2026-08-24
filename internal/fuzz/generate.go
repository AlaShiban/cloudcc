// Package fuzz generates idiomatic Python programs that use the CloudCompiler
// SDK, together with the ground truth of what a correct compiler should find
// in them.
//
// The point is coverage of *shape*. The compiler reads syntax rather than
// running code, so a hint written in a form it does not recognise becomes a
// resource that silently does not exist -- the worst possible failure, because
// nothing complains until production. Generating the same program in many
// syntactic disguises is the cheapest way to find those blind spots.
//
// Every program carries its own Expectations, which turns the oracle from "did
// it compile" into "did it find exactly what I planted, and nothing else".
package fuzz

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
)

// Capability kinds, matching config.Kind* without importing the compiler into
// the generator.
const (
	KindPersistKV     = "persist_kv"
	KindPersistFS     = "persist_fs"
	KindPersistSecret = "persist_secret"
	KindPersistORM    = "persist_orm"
	KindPersistRedis  = "persist_redis"
	KindPubSub        = "pubsub"
	KindConfig        = "config"
	KindStaticUnit    = "static_unit"
)

// Options controls what a generated program is allowed to contain.
type Options struct {
	// Behavioural restricts generation to what the differential harness can
	// actually run and compare: Lambda units, and stores whose emulated and
	// real implementations are observably equivalent.
	Behavioural bool
	// AllowContainer permits ECS units fronted by an ALB.
	AllowContainer bool
}

// Route is one HTTP route the compiler is expected to discover.
type Route struct {
	Verb string
	Path string
}

// GatewayExpect is the expected shape of one exposed gateway.
type GatewayExpect struct {
	Unit   string
	Routes []Route
}

// ConfigExpect is an expected configuration value.
type ConfigExpect struct {
	Default string
	Secret  bool
}

// Expectations is what a correct compile must produce from a program. The
// generator knows the truth because it planted it.
type Expectations struct {
	Units       []string
	Gateways    map[string]GatewayExpect
	Stores      map[string]string
	Topics      []string
	ConfigVars  map[string]ConfigExpect
	StaticUnits []string

	// EntryFile maps a unit to the module that declares it.
	EntryFile map[string]string
	// SharedModules must appear in every unit's bundle.
	SharedModules []string
	// ClaimedFiles were taken by a static unit and must appear in no bundle.
	ClaimedFiles []string
	// EmbeddedFiles maps a unit to files embed_assets bound to it alone.
	EmbeddedFiles map[string][]string
	// ComputeTypes maps a unit to its configured compute type.
	ComputeTypes map[string]string
}

// Step is one request the differential harness issues.
type Step struct {
	Method string
	Path   string
	Body   string
	// Store names the store this step touches, so the harness knows which
	// backing state to compare afterwards.
	Store string
}

// Program is a generated application plus the truth about it.
type Program struct {
	Seed   int64
	Name   string
	Files  map[string]string
	Config string
	Expect Expectations
	// Scenario drives the differential harness against the exposed unit.
	Scenario []Step
	// PrimaryUnit is the exposed unit the scenario targets.
	PrimaryUnit string
	// PrimaryStore is the KV store the scenario reads and writes.
	PrimaryStore string
	// RoutePrefix is the base path the exposed unit serves items under.
	RoutePrefix string
	// EntryModule is the dotted module of the exposed unit's entrypoint, and
	// AppVar the module-level name of its ASGI application. The differential
	// harness needs both to run the program before it is compiled.
	EntryModule string
	AppVar      string
}

// layout decides how modules are arranged on disk.
type layout int

const (
	layoutFlat    layout = iota // api.py, store.py
	layoutPackage               // app/__init__.py, app/api.py, app/store.py
	layoutNested                // src/__init__.py, src/svc/__init__.py, ...
)

// Generate builds a program deterministically from a seed. The same seed
// always produces the same program, so a failure is always reproducible.
func Generate(seed int64, opts Options) *Program {
	rng := rand.New(rand.NewSource(seed))
	g := &generator{rng: rng, opts: opts, files: map[string]string{}}
	return g.build(seed)
}

type generator struct {
	rng   *rand.Rand
	opts  Options
	files map[string]string

	pkgDir string
	lay    layout
}

func (g *generator) build(seed int64) *Program {
	g.lay = layout(g.rng.Intn(3))
	if g.opts.Behavioural && g.lay == layoutNested {
		// Nested packages are fine to compile but make running the program
		// from its bundle root fiddly, which the differential harness needs.
		g.lay = layoutPackage
	}
	switch g.lay {
	case layoutPackage:
		g.pkgDir = "svc"
	case layoutNested:
		g.pkgDir = "src/svc"
	}

	p := &Program{
		Seed: seed,
		Name: fmt.Sprintf("gen-%d", seed),
		Expect: Expectations{
			Gateways:      map[string]GatewayExpect{},
			Stores:        map[string]string{},
			ConfigVars:    map[string]ConfigExpect{},
			EntryFile:     map[string]string{},
			EmbeddedFiles: map[string][]string{},
			ComputeTypes:  map[string]string{},
		},
	}

	unitCount := 1 + g.rng.Intn(3)
	multi := unitCount > 1
	// A lone unit is usually left undeclared -- that is the common shape, and
	// the compiler names it "main". Declaring it anyway is also idiomatic, so
	// both are generated.
	declare := multi || g.rng.Intn(2) == 0

	// ------------------------------------------------------ shared stores
	storeMod := newModule(g.rng)
	storeNames := map[string]string{} // store id -> python variable
	kinds := g.pickKinds()

	for i, kind := range kinds {
		id := makeID(g.rng, storeBase(kind), i)
		v := pyIdentifier(id)
		storeNames[id] = v
		storeMod.bindStore(g.rng, v, storeMod.hint(sdkFunc(kind), g.storeArgs(storeMod, kind, id)...))
		p.Expect.Stores[id] = kind
	}

	topicID := ""
	if multi && g.rng.Intn(2) == 0 {
		topicID = makeID(g.rng, "events", g.rng.Intn(9))
		storeMod.assign("events", storeMod.hint("pubsub_topic", pos(storeMod.quote(topicID))))
		p.Expect.Topics = append(p.Expect.Topics, topicID)
	}

	storeMod.blank()
	storeMod.line("def describe() -> dict:")
	storeMod.line(`    """Shared helper, so every unit really does import this module."""`)
	storeMod.linef("    return {%s}", strings.Join(quotedPairs(storeNames), ", "))

	storePath := g.path("store.py")
	g.files[storePath] = storeMod.render("Stores shared by every execution unit.")
	p.Expect.SharedModules = append(p.Expect.SharedModules, storePath)
	if init := g.packageInit(); init != "" {
		p.Expect.SharedModules = append(p.Expect.SharedModules, init)
	}

	// ------------------------------------------------------ config values
	configCount := g.rng.Intn(3)
	cfgVars := map[string]ConfigExpect{}
	for i := 0; i < configCount; i++ {
		id := makeID(g.rng, "setting", i)
		expect := ConfigExpect{}
		if g.rng.Intn(2) == 0 {
			expect.Default = fmt.Sprintf("value-%d", i)
		}
		if g.rng.Intn(4) == 0 {
			expect.Secret = true
			if g.opts.Behavioural && expect.Default == "" {
				// The differential harness deploys unattended, so a secret it
				// generates always carries a fallback.
				expect.Default = fmt.Sprintf("secret-%d", i)
			}
		}
		cfgVars[id] = expect
		p.Expect.ConfigVars[id] = expect
	}

	// ------------------------------------------------------ units
	primary := 0
	units := make([]string, 0, unitCount)
	if !declare {
		units = append(units, "main")
	} else {
		for i := 0; i < unitCount; i++ {
			units = append(units, makeID(g.rng, unitBase(i), i))
		}
	}
	sort.Strings(units)
	p.Expect.Units = units
	p.PrimaryUnit = units[primary]

	primaryStore := g.pickKVStore(storeNames, p.Expect.Stores)
	p.PrimaryStore = primaryStore

	// Static and embedded assets are decided before any module is written, so
	// their declarations are part of the module rather than spliced into
	// already-rendered text.
	var static *assetSpec
	if g.rng.Intn(3) == 0 {
		static = g.planStaticUnit(p)
	}
	var embedded *assetSpec
	if g.rng.Intn(3) == 0 {
		embedded = g.planEmbeddedAssets(p, p.PrimaryUnit)
	}

	for i, unitID := range units {
		exposed := i == primary
		var s, e *assetSpec
		if exposed {
			s, e = static, embedded
		}
		g.writeUnit(p, unitID, exposed, declare, storeNames, cfgVars, topicID, primaryStore, i, s, e)
	}

	// A helper module that imports the SDK but calls nothing: the import still
	// has to be stripped, because the SDK is not installed in a bundle.
	if imp := unusedSDKImport(g.rng); imp != "" {
		helper := g.path("helpers.py")
		g.files[helper] = imp + "\n\n\ndef noop() -> None:\n    return None\n"
		p.Expect.SharedModules = append(p.Expect.SharedModules, helper)
		for _, unit := range p.Expect.Units {
			entry := p.Expect.EntryFile[unit]
			g.files[entry] = addHelperImport(g.files[entry], g.helperImportSpec())
		}
	}

	p.EntryModule = moduleName(p.Expect.EntryFile[p.PrimaryUnit])
	g.applyFileShapes()
	g.files["requirements.txt"] = "fastapi==0.115.6\n"
	p.Config = g.renderConfig(p)
	p.Files = g.files
	p.Scenario = g.scenario(primaryStore, p.RoutePrefix)
	return p
}

// pickKinds chooses which store capabilities appear. The differential harness
// can only compare stores whose emulated and real behaviour is observably the
// same, so it gets a narrower set.
func (g *generator) pickKinds() []string {
	var pool []string
	if g.opts.Behavioural {
		pool = []string{KindPersistKV, KindPersistFS}
	} else {
		pool = []string{
			KindPersistKV, KindPersistFS, KindPersistSecret,
			KindPersistORM, KindPersistRedis,
		}
	}
	// Always at least one KV store: it is what the HTTP surface is built on.
	kinds := []string{KindPersistKV}
	extra := g.rng.Intn(3)
	for i := 0; i < extra; i++ {
		kinds = append(kinds, pool[g.rng.Intn(len(pool))])
	}
	return kinds
}

// hintMaybeCommented renders a hint call, occasionally with comments between
// its arguments so the rewriter has to splice a span containing non-code bytes.
func (g *generator) hintMaybeCommented(m *pyModule, fn string, args ...arg) string {
	if len(args) > 1 && g.rng.Intn(5) == 0 {
		return m.commentedCall(m.localName(fn), args)
	}
	return m.hint(fn, args...)
}

func (g *generator) storeArgs(m *pyModule, kind, id string) []arg {
	args := []arg{}
	if g.rng.Intn(3) == 0 {
		args = append(args, kw("id", m.quote(id)))
	} else {
		args = append(args, pos(m.quote(id)))
	}
	if kind == KindPersistORM && g.rng.Intn(2) == 0 {
		args = append(args, kw("models", `["Row"]`))
	}
	return args
}

func (g *generator) pickKVStore(names map[string]string, kinds map[string]string) string {
	ids := make([]string, 0, len(names))
	for id := range names {
		if kinds[id] == KindPersistKV {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids[0]
}

// writeUnit emits one execution unit's entry module.
func (g *generator) writeUnit(p *Program, unitID string, exposed, declare bool,
	storeNames map[string]string, cfgVars map[string]ConfigExpect,
	topicID, primaryStore string, index int, static, embedded *assetSpec) {

	m := newModule(g.rng)
	entry := g.path(pyIdentifier(unitID) + ".py")
	p.Expect.EntryFile[unitID] = entry

	if declare {
		args := []arg{kw("id", m.quote(unitID))}
		m.line(m.hint("execution_unit", args...))
		m.blank()
	}

	computeType := "lambda"
	if g.opts.AllowContainer && exposed && g.rng.Intn(3) == 0 {
		computeType = "ecs"
	}
	p.Expect.ComputeTypes[unitID] = computeType

	// Import the shared stores, in whichever style suits the layout.
	m.importLocal(g.importSpec(storeImportNames(storeNames, topicID)))

	// Configuration values: only the first unit declares them, so the
	// expectations stay unambiguous about which unit uses what.
	if index == 0 {
		for _, id := range sortedKeys(cfgVars) {
			expect := cfgVars[id]
			args := []arg{pos(m.quote(id))}
			if expect.Default != "" {
				args = append(args, kw("default", m.quote(expect.Default)))
			}
			if expect.Secret {
				args = append(args, kw("secret", "True"))
			}
			m.assign(pyIdentifier(id), g.hintMaybeCommented(m, "config_value", args...))
		}
		if len(cfgVars) > 0 {
			m.blank()
		}
	}

	if static != nil {
		m.line(m.hint("static_unit",
			pos(m.quote(static.id)),
			kw("static_files", m.quote(static.glob(entry))),
		))
		m.blank()
	}
	if embedded != nil {
		m.assign("SEED_GLOB", m.hint("embed_assets", pos(m.quote(embedded.glob(entry)))))
		m.blank()
	}

	if exposed {
		g.writeExposedBody(p, m, unitID, storeNames, primaryStore, topicID)
	} else {
		g.writeWorkerBody(m, storeNames, primaryStore, topicID)
	}

	g.files[entry] = m.render(fmt.Sprintf("Execution unit %s.", unitID))
}

func (g *generator) writeExposedBody(p *Program, m *pyModule, unitID string,
	storeNames map[string]string, primaryStore, topicID string) {

	m.importStd("from fastapi import FastAPI, HTTPException")
	appVar := []string{"app", "api", "application"}[g.rng.Intn(3)]
	m.assign(appVar, "FastAPI()")
	p.AppVar = appVar

	gatewayID := makeID(g.rng, "gw", g.rng.Intn(9))
	exposeArgs := []arg{pos(appVar)}
	if g.rng.Intn(3) > 0 {
		exposeArgs = append(exposeArgs, kw("id", m.quote(gatewayID)))
	} else {
		gatewayID = "main" // the SDK's default when no id is given
	}
	m.line(m.hint("expose", exposeArgs...))
	m.blank()

	store := storeNames[primaryStore]
	routes := []Route{}
	base := routeSet(g.rng)
	p.RoutePrefix = base

	m.blank()
	m.line(tracedDecorator)
	m.blank()

	m.decorateHandler(g.rng, appVar, "GET", "/health", "health() -> dict",
		[]string{`return {"status": "ok"}`})
	routes = append(routes, Route{"GET", "/health"})
	m.blank()

	m.decorateHandler(g.rng, appVar, "GET", base+"/{item_id}", "read_item(item_id: str) -> dict",
		[]string{
			fmt.Sprintf("found = %s.get(item_id)", store),
			"if found is None:",
			`    raise HTTPException(status_code=404, detail="missing")`,
			"return found",
		})
	routes = append(routes, Route{"GET", base + "/{item_id}"})
	m.blank()

	writeBody := []string{fmt.Sprintf("%s.put(item_id, payload)", store)}
	if topicID != "" {
		writeBody = append(writeBody, `events.publish({"id": item_id})`)
	}
	writeBody = append(writeBody, `return {"ok": True, "id": item_id}`)
	m.decorateHandler(g.rng, appVar, "PUT", base+"/{item_id}",
		"write_item(item_id: str, payload: dict) -> dict", writeBody)
	routes = append(routes, Route{"PUT", base + "/{item_id}"})
	m.blank()

	m.decorateHandler(g.rng, appVar, "DELETE", base+"/{item_id}", "drop_item(item_id: str) -> dict",
		[]string{
			fmt.Sprintf("%s.delete(item_id)", store),
			`return {"ok": True}`,
		})
	routes = append(routes, Route{"DELETE", base + "/{item_id}"})
	m.blank()

	m.decorateHandler(g.rng, appVar, "GET", base, "list_items() -> dict",
		[]string{fmt.Sprintf(`return {"keys": sorted(%s.keys())}`, store)})
	routes = append(routes, Route{"GET", base})

	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Path != routes[j].Path {
			return routes[i].Path < routes[j].Path
		}
		return routes[i].Verb < routes[j].Verb
	})
	p.Expect.Gateways[gatewayID] = GatewayExpect{Unit: unitID, Routes: routes}
}

func (g *generator) writeWorkerBody(m *pyModule, storeNames map[string]string,
	primaryStore, topicID string) {

	store := storeNames[primaryStore]
	m.blank()
	m.line("def summarise() -> int:")
	m.linef("    return len(%s.keys())", store)

	if topicID != "" {
		m.blank()
		m.line("def on_event(message: dict) -> dict:")
		m.line(`    return {"seen": message.get("id")}`)
		m.blank()
		m.line("events.subscribe(on_event)")
	}
}

// assetSpec is a bundle of generated files plus the glob that claims them.
type assetSpec struct {
	id    string
	dir   string
	files []string
	// pattern is appended to the directory, e.g. "/**/*".
	pattern string
}

// glob renders the claim relative to the module that declares it, which is how
// a person writes a path next to their own file.
func (a *assetSpec) glob(declaringFile string) string {
	depth := strings.Count(declaringFile, "/")
	prefix := "./"
	if depth > 0 {
		prefix = strings.Repeat("../", depth)
	}
	return prefix + a.dir + a.pattern
}

func (g *generator) planStaticUnit(p *Program) *assetSpec {
	spec := &assetSpec{
		id:      makeID(g.rng, "site", g.rng.Intn(9)),
		dir:     "public",
		pattern: "/**/*",
		files: []string{
			"public/index.html",
			"public/app.css",
			"public/assets/logo.svg",
		},
	}
	for i, f := range spec.files {
		g.files[f] = fmt.Sprintf("<!-- generated asset %d -->\n", i)
	}
	p.Expect.StaticUnits = append(p.Expect.StaticUnits, spec.id)
	p.Expect.ClaimedFiles = append(p.Expect.ClaimedFiles, spec.files...)
	return spec
}

func (g *generator) planEmbeddedAssets(p *Program, unitID string) *assetSpec {
	spec := &assetSpec{
		dir:     "seed",
		pattern: "/*.json",
		files:   []string{"seed/one.json", "seed/two.json"},
	}
	for i, f := range spec.files {
		g.files[f] = fmt.Sprintf("{\"n\": %d}\n", i)
	}
	p.Expect.EmbeddedFiles[unitID] = spec.files
	return spec
}

// ---------------------------------------------------------------- layout

func (g *generator) path(name string) string {
	if g.pkgDir == "" {
		return name
	}
	return g.pkgDir + "/" + name
}

// packageInit creates the __init__.py files a package layout needs and returns
// the innermost one.
func (g *generator) packageInit() string {
	if g.pkgDir == "" {
		return ""
	}
	parts := strings.Split(g.pkgDir, "/")
	last := ""
	for i := range parts {
		p := strings.Join(parts[:i+1], "/") + "/__init__.py"
		g.files[p] = ""
		last = p
	}
	return last
}

// importSpec renders the import of the shared store module, relative inside a
// package and absolute when flat.
func (g *generator) importSpec(names []string) string {
	joined := strings.Join(names, ", ")
	if g.pkgDir == "" {
		if g.rng.Intn(2) == 0 {
			return "from store import " + joined
		}
		return "from store import " + joined
	}
	if g.rng.Intn(2) == 0 {
		return "from .store import " + joined
	}
	return "from " + strings.ReplaceAll(g.pkgDir, "/", ".") + ".store import " + joined
}

func (g *generator) renderConfig(p *Program) string {
	var b strings.Builder
	b.WriteString("app: " + p.Name + "\n")
	b.WriteString("provider: aws\n")

	nonDefault := false
	for _, unit := range p.Expect.Units {
		if p.Expect.ComputeTypes[unit] != "lambda" {
			nonDefault = true
		}
	}
	if nonDefault {
		b.WriteString("\nexecution_units:\n")
		for _, unit := range p.Expect.Units {
			b.WriteString("  " + unit + ":\n    type: " + p.Expect.ComputeTypes[unit] + "\n")
		}
		b.WriteString("\nexposed:\n")
		for id, gw := range p.Expect.Gateways {
			typ := "apigateway"
			if p.Expect.ComputeTypes[gw.Unit] == "ecs" {
				typ = "alb"
			}
			b.WriteString("  " + id + ":\n    type: " + typ + "\n")
		}
	}
	return b.String()
}

// scenario is the request sequence the differential harness replays against
// both the uncompiled and the compiled program.
func (g *generator) scenario(store, base string) []Step {
	if base == "" {
		base = "/items"
	}
	return []Step{
		{Method: "GET", Path: "/health"},
		{Method: "GET", Path: base, Store: store},
		{Method: "GET", Path: base + "/alpha", Store: store},
		{Method: "PUT", Path: base + "/alpha", Body: `{"name":"first","n":1}`, Store: store},
		{Method: "PUT", Path: base + "/beta", Body: `{"name":"second","n":2}`, Store: store},
		{Method: "GET", Path: base + "/alpha", Store: store},
		{Method: "GET", Path: base, Store: store},
		{Method: "DELETE", Path: base + "/alpha", Store: store},
		{Method: "GET", Path: base + "/alpha", Store: store},
		{Method: "GET", Path: base, Store: store},
	}
}

// applyFileShapes varies the physical form of the generated files. Every byte
// offset the compiler records has to survive carriage returns, tabs and
// trailing whitespace, none of which change what the program means.
func (g *generator) applyFileShapes() {
	// Sorted, not map order: every rng draw has to happen in the same sequence
	// on every run, or a seed stops reproducing the program it produced last
	// time -- which is the one property this whole approach rests on.
	for _, path := range sortedKeys(g.files) {
		src := g.files[path]
		if !strings.HasSuffix(path, ".py") {
			continue
		}
		switch g.rng.Intn(8) {
		case 0:
			g.files[path] = withCRLF(src)
		case 1:
			g.files[path] = withTabs(src)
		case 2:
			g.files[path] = trailingWhitespace(g.rng, src)
		}
	}
}

// helperImportSpec imports the helper module in whichever form the layout
// calls for.
func (g *generator) helperImportSpec() string {
	if g.pkgDir == "" {
		return "from helpers import noop"
	}
	if g.rng.Intn(2) == 0 {
		return "from .helpers import noop"
	}
	return "from " + strings.ReplaceAll(g.pkgDir, "/", ".") + ".helpers import noop"
}

// addHelperImport appends an import to a rendered module, after its existing
// imports.
func addHelperImport(src, spec string) string {
	lines := strings.Split(src, "\n")
	last := -1
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "from ") && strings.Contains(t, " import ") {
			last = i
		} else if strings.HasPrefix(t, "import ") {
			last = i
		}
	}
	if last < 0 {
		return spec + "\n" + src
	}
	out := append([]string{}, lines[:last+1]...)
	out = append(out, spec)
	out = append(out, lines[last+1:]...)
	return strings.Join(out, "\n")
}

// ---------------------------------------------------------------- helpers

// moduleName turns a source path into the dotted module a runtime import uses.
func moduleName(path string) string {
	path = strings.TrimSuffix(path, ".py")
	if strings.HasSuffix(path, "/__init__") {
		path = strings.TrimSuffix(path, "/__init__")
	}
	return strings.ReplaceAll(path, "/", ".")
}

func sdkFunc(kind string) string {
	switch kind {
	case KindPubSub:
		return "pubsub_topic"
	case KindConfig:
		return "config_value"
	}
	return kind
}

func storeBase(kind string) string {
	switch kind {
	case KindPersistKV:
		return "records"
	case KindPersistFS:
		return "documents"
	case KindPersistSecret:
		return "signingKey"
	case KindPersistORM:
		return "ledger"
	case KindPersistRedis:
		return "sessions"
	}
	return "store"
}

func unitBase(i int) string {
	return []string{"api", "worker", "reporter"}[i%3]
}

func storeImportNames(storeNames map[string]string, topicID string) []string {
	names := make([]string, 0, len(storeNames)+1)
	for _, v := range storeNames {
		names = append(names, v)
	}
	sort.Strings(names)
	if topicID != "" {
		names = append(names, "events")
	}
	return names
}

func quotedPairs(storeNames map[string]string) []string {
	ids := make([]string, 0, len(storeNames))
	for id := range storeNames {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, fmt.Sprintf("%q: repr(%s)", id, storeNames[id]))
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
