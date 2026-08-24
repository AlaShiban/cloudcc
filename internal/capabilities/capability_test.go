package capabilities

import (
	"reflect"
	"testing"

	"github.com/cloudcompiler/cloudcc/internal/compiler"
	"github.com/cloudcompiler/cloudcc/internal/config"
	"github.com/cloudcompiler/cloudcc/internal/ir"
)

func edgeStrings(ctx *compiler.Context) []string {
	var out []string
	for _, e := range ctx.Graph.Edges() {
		out = append(out, e.From.String()+" -"+e.Kind+"-> "+e.To.String())
	}
	return out
}

func TestPersistIntentIsSharedBetweenUnits(t *testing.T) {
	ctx := harness(t, map[string]string{
		"api.py":             "import cloudcompiler as cloudcc\ncloudcc.execution_unit(id=\"api\")\nfrom shared.store import pets\n",
		"worker.py":          "import cloudcompiler as cloudcc\ncloudcc.execution_unit(id=\"worker\")\nfrom shared.store import pets\n",
		"shared/__init__.py": "",
		"shared/store.py":    "import cloudcompiler as cloudcc\npets = cloudcc.persist(cloudcc.KVStore(), id=\"petsByOwner\")\n",
	})

	stores := ctx.Graph.IntentsOfKind(config.KindPersistKV)
	if len(stores) != 1 {
		t.Fatalf("got %d persist_kv intents, want exactly one shared node", len(stores))
	}
	key := stores[0].Key()
	if key.ID != "petsByOwner" {
		t.Errorf("key = %v", key)
	}
	users := ctx.Graph.EdgesTo(key, ir.EdgeUses)
	if len(users) != 2 {
		t.Fatalf("uses edges = %v, want one per unit", edgeStrings(ctx))
	}
	for _, want := range []string{"api", "worker"} {
		found := false
		for _, e := range users {
			if e.From.ID == want {
				found = true
			}
		}
		if !found {
			t.Errorf("unit %q has no uses edge: %v", want, edgeStrings(ctx))
		}
	}
	if stores[0].Config().Type != "dynamodb" {
		t.Errorf("resolved type = %q, want dynamodb", stores[0].Config().Type)
	}
}

// The client a program reached for is the weakest layer of configuration. It
// supplies the default -- a Redis client asks for ElastiCache -- but an
// explicit cloudcc.yaml entry still wins, so moving to MemoryDB stays a
// configuration change rather than a code change.
func TestClientTypeIsADefaultThatConfigOverrides(t *testing.T) {
	src := "from redis import Redis\n\nimport cloudcompiler as cloudcc\n\ncache = cloudcc.persist(Redis(), id=\"sessions\")\n"

	ctx := harness(t, map[string]string{"app.py": src})
	store := ctx.Graph.IntentsOfKind(config.KindPersistRedis)[0]
	if got := store.Config().Type; got != "elasticache" {
		t.Errorf("type = %q, want elasticache from the Redis client", got)
	}
	// The decision is recorded so compiled/cloudcc.yaml documents it.
	if got := ctx.Config.Persisted["sessions"].Type; got != "elasticache" {
		t.Errorf("recorded type = %q", got)
	}

	configured := harnessWithConfig(t,
		map[string]string{"app.py": src},
		"app: t\npersisted:\n  sessions:\n    type: memorydb\n")
	store = configured.Graph.IntentsOfKind(config.KindPersistRedis)[0]
	if got := store.Config().Type; got != "memorydb" {
		t.Errorf("type = %q, want the explicitly configured memorydb", got)
	}
}

func TestSameIDUnderTwoPersistKindsIsAnError(t *testing.T) {
	ctx := harnessAllowingDiags(t, map[string]string{
		"app.py": "import cloudcompiler as cloudcc\n" +
			"a = cloudcc.persist(cloudcc.KVStore(), id=\"thing\")\n" +
			"b = cloudcc.persist(Path(\"./data\"), id=\"thing\")\n",
	})
	if !containsSubstr(diagStrings(ctx), "each id names one store") {
		t.Errorf("diagnostics = %v", diagStrings(ctx))
	}
}

func TestExposeDiscoversRoutes(t *testing.T) {
	ctx := harness(t, map[string]string{
		"app.py": `from fastapi import FastAPI
import cloudcompiler as cloudcc

app = FastAPI()
cloudcc.expose(app, id="pet-api")

@app.get("/pets/{pet_id}")
def get_pet(pet_id: str):
    return {}

@app.put("/pets/{pet_id}")
def put_pet(pet_id: str):
    return {}

@app.get("/health")
def health():
    return {}
`,
	})
	gws := ctx.Graph.IntentsOfKind(config.KindExpose)
	if len(gws) != 1 {
		t.Fatalf("got %d expose intents", len(gws))
	}
	gw := gws[0].(*ir.Expose)
	want := []ir.Route{
		{Verb: "GET", Path: "/health"},
		{Verb: "GET", Path: "/pets/{pet_id}"},
		{Verb: "PUT", Path: "/pets/{pet_id}"},
	}
	if !reflect.DeepEqual(gw.Routes, want) {
		t.Errorf("routes = %v, want %v", gw.Routes, want)
	}
	if gw.Unit != DefaultUnitID || gw.Target != "public" {
		t.Errorf("gateway = %+v", gw)
	}

	unit := ctx.Graph.IntentsOfKind(config.KindExecutionUnit)[0].(*ir.ExecUnit)
	if unit.ASGIApp != "app" {
		t.Errorf("ASGIApp = %q, want app", unit.ASGIApp)
	}
	if !containsStr(edgeStrings(ctx), "expose/pet-api -exposes-> execution_unit/main") {
		t.Errorf("edges = %v", edgeStrings(ctx))
	}
}

func TestExposeIgnoresDecoratorsOnOtherObjects(t *testing.T) {
	ctx := harness(t, map[string]string{
		"app.py": `from fastapi import FastAPI
import cloudcompiler as cloudcc

app = FastAPI()
other = FastAPI()
cloudcc.expose(app, id="a")

@other.get("/not-ours")
def x():
    return {}

@app.get("/ours")
def y():
    return {}
`,
	})
	gw := ctx.Graph.IntentsOfKind(config.KindExpose)[0].(*ir.Expose)
	if !reflect.DeepEqual(gw.Routes, []ir.Route{{Verb: "GET", Path: "/ours"}}) {
		t.Errorf("routes = %v", gw.Routes)
	}
}

func TestExposeWarnsAboutAPIRouter(t *testing.T) {
	ctx := harness(t, map[string]string{
		"app.py": `from fastapi import FastAPI, APIRouter
import cloudcompiler as cloudcc

app = FastAPI()
router = APIRouter()
cloudcc.expose(app, id="a")

@app.get("/ours")
def y():
    return {}
`,
	})
	if !containsSubstr(diagStrings(ctx), "routes registered on a router are not detected") {
		t.Errorf("expected an APIRouter warning, got %v", diagStrings(ctx))
	}
}

func TestExposeAcrossTwoUnitsIsAnError(t *testing.T) {
	ctx := harnessAllowingDiags(t, map[string]string{
		"api.py":             "import cloudcompiler as cloudcc\ncloudcc.execution_unit(id=\"api\")\nimport shared.web\n",
		"worker.py":          "import cloudcompiler as cloudcc\ncloudcc.execution_unit(id=\"worker\")\nimport shared.web\n",
		"shared/__init__.py": "",
		"shared/web.py": "from fastapi import FastAPI\nimport cloudcompiler as cloudcc\n" +
			"app = FastAPI()\ncloudcc.expose(app, id=\"api\")\n",
	})
	if !containsSubstr(diagStrings(ctx), "must belong to exactly one") {
		t.Errorf("diagnostics = %v", diagStrings(ctx))
	}
}

func TestPubSubDistinguishesPublishersFromSubscribers(t *testing.T) {
	ctx := harness(t, map[string]string{
		"api.py": "import cloudcompiler as cloudcc\ncloudcc.execution_unit(id=\"api\")\n" +
			"from shared.bus import events\ndef go():\n    events.publish({\"a\": 1})\n",
		"worker.py": "import cloudcompiler as cloudcc\ncloudcc.execution_unit(id=\"worker\")\n" +
			"from shared.bus import events\ndef on(m):\n    return m\nevents.subscribe(on)\n",
		"shared/__init__.py": "",
		"shared/bus.py":      "import cloudcompiler as cloudcc\nevents = cloudcc.persist(cloudcc.Topic(), id=\"petEvents\")\n",
	})
	edges := edgeStrings(ctx)
	for _, want := range []string{
		"execution_unit/api -publishes-> pubsub/petEvents",
		"execution_unit/worker -subscribes-> pubsub/petEvents",
	} {
		if !containsStr(edges, want) {
			t.Errorf("missing edge %q in %v", want, edges)
		}
	}
	if containsStr(edges, "execution_unit/api -subscribes-> pubsub/petEvents") {
		t.Errorf("api should not be recorded as a subscriber: %v", edges)
	}
	if containsStr(edges, "execution_unit/worker -publishes-> pubsub/petEvents") {
		t.Errorf("worker should not be recorded as a publisher: %v", edges)
	}
}

func TestConfigValueBecomesAnIntentWithUsesEdges(t *testing.T) {
	ctx := harness(t, map[string]string{
		"app.py": "import cloudcompiler as cloudcc\n" +
			"level = cloudcc.config_value(\"log_level\", default=\"info\")\n" +
			"key = cloudcc.config_value(\"api_key\", secret=True)\n",
	})
	vars := ctx.Graph.IntentsOfKind(config.KindConfig)
	if len(vars) != 2 {
		t.Fatalf("got %d config vars", len(vars))
	}
	byID := map[string]*ir.ConfigVar{}
	for _, v := range vars {
		byID[v.Key().ID] = v.(*ir.ConfigVar)
	}
	if byID["log_level"].Default != "info" || byID["log_level"].Secret {
		t.Errorf("log_level = %+v", byID["log_level"])
	}
	if !byID["api_key"].Secret {
		t.Errorf("api_key should be a secret: %+v", byID["api_key"])
	}
	for _, id := range []string{"log_level", "api_key"} {
		want := "execution_unit/main -uses-> config/" + id
		if !containsStr(edgeStrings(ctx), want) {
			t.Errorf("missing %q in %v", want, edgeStrings(ctx))
		}
	}
}

func TestConflictingSecrecyIsAnError(t *testing.T) {
	ctx := harnessAllowingDiags(t, map[string]string{
		"app.py": "import cloudcompiler as cloudcc\n" +
			"a = cloudcc.config_value(\"k\", secret=True)\n" +
			"b = cloudcc.config_value(\"k\")\n",
	})
	if !containsSubstr(diagStrings(ctx), "both as a secret and as a plain value") {
		t.Errorf("diagnostics = %v", diagStrings(ctx))
	}
}

func TestOnlyCapabilityPluginsCreateIntents(t *testing.T) {
	// The two IR layers stay separate until the provider resolver runs (D7):
	// nothing in this package may add a concrete resource.
	root := t.TempDir()
	write(t, root, "app.py", "import cloudcompiler as cloudcc\npets = cloudcc.persist(cloudcc.KVStore(), id=\"a\")\n")
	ctx := compileWith(t, root, IntentChain())
	if got := len(ctx.Graph.Resources()); got != 0 {
		t.Errorf("capability plugins created %d concrete resources; only resolve:aws may", got)
	}
}

func harnessAllowingDiags(t *testing.T, files map[string]string) *compiler.Context {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		write(t, root, rel, content)
	}
	return compileExpectingDiags(t, root)
}

func containsStr(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// A route path is a string literal like any other, and people write long ones
// across lines. A second, simpler decoder in this file used to miss those,
// which is why route reading now shares the one in sdkdetect.
func TestRoutePathsInEveryStringForm(t *testing.T) {
	ctx := harness(t, map[string]string{
		"app.py": `from fastapi import FastAPI
import cloudcompiler as cloudcc

app = FastAPI()
cloudcc.expose(app, id="gw")

@app.get("/plain")
def a():
    return {}

@app.get("/con" "catenated")
def b():
    return {}

@app.get(
    (
        "/paren"
        "thesised"
    )
)
def c():
    return {}

@app.post('/single-quoted')
def d():
    return {}
`,
	})
	gw := ctx.Graph.IntentsOfKind(config.KindExpose)[0].(*ir.Expose)
	want := []ir.Route{
		{Verb: "GET", Path: "/concatenated"},
		{Verb: "GET", Path: "/parenthesised"},
		{Verb: "GET", Path: "/plain"},
		{Verb: "POST", Path: "/single-quoted"},
	}
	if !reflect.DeepEqual(gw.Routes, want) {
		t.Errorf("routes =\n  %v\nwant\n  %v", gw.Routes, want)
	}
}

// An f-string path is genuinely not knowable at compile time and must stay
// undiscovered rather than being guessed at.
func TestDynamicRoutePathIsNotInvented(t *testing.T) {
	ctx := harness(t, map[string]string{
		"app.py": `from fastapi import FastAPI
import cloudcompiler as cloudcc

app = FastAPI()
cloudcc.expose(app, id="gw")
prefix = "/v1"

@app.get(f"{prefix}/items")
def a():
    return {}

@app.get("/known")
def b():
    return {}
`,
	})
	gw := ctx.Graph.IntentsOfKind(config.KindExpose)[0].(*ir.Expose)
	if !reflect.DeepEqual(gw.Routes, []ir.Route{{Verb: "GET", Path: "/known"}}) {
		t.Errorf("routes = %v; an f-string path should not be guessed", gw.Routes)
	}
}
