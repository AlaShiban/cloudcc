package capabilities

import (
	"strings"
	"testing"

	"github.com/spf13/afero"

	"github.com/cloudcompiler/cloudcc/internal/compiler"
	"github.com/cloudcompiler/cloudcc/internal/config"
	"github.com/cloudcompiler/cloudcc/internal/ir"
)

// A two-unit program where the storefront awaits a function on the pricing
// unit. The shape is the one every test in this file varies.
func remoteProgram(caller, callee string) map[string]string {
	return map[string]string{
		"nomnom/__init__.py": "",
		"nomnom/pricing.py":  callee,
		"storefront.py":      caller,
	}
}

const calleeSource = `import cloudcompiler as cloudcc

cloudcc.execution_unit(id="pricing")


async def quote(items):
    return {"total": len(items)}


def _internal():
    return 1
`

const callerSource = `import cloudcompiler as cloudcc

from nomnom import pricing

cloudcc.execution_unit(id="storefront")

pricing = cloudcc.remote(pricing, id="pricing")


async def order(items):
    return await pricing.quote(items)
`

func TestARemoteCallBecomesACallsEdge(t *testing.T) {
	ctx := harness(t, remoteProgram(callerSource, calleeSource))
	if errs := ctx.Diags.HasErrors(); errs {
		t.Fatalf("diagnostics: %v", diagStrings(ctx))
	}

	from := ir.Key{Kind: config.KindExecutionUnit, ID: "storefront"}
	to := ir.Key{Kind: config.KindExecutionUnit, ID: "pricing"}
	if len(ctx.Graph.EdgesFrom(from, ir.EdgeCalls)) != 1 {
		t.Errorf("no calls edge storefront -> pricing: %v", edgeStrings(ctx))
	}
	// The uses edge is what environment wiring and IAM read, everywhere.
	found := false
	for _, e := range ctx.Graph.EdgesFrom(from, ir.EdgeUses) {
		if e.To == to {
			found = true
		}
	}
	if !found {
		t.Errorf("no uses edge storefront -> pricing: %v", edgeStrings(ctx))
	}
}

// The point of the boundary. Without the closure cut the caller would bundle
// the callee's module, and every store the callee declares would pick up a
// uses edge from the caller -- so the caller would be granted IAM on tables it
// has no business touching, and would carry the callee's dependencies.
func TestTheCallerDoesNotBundleTheCallee(t *testing.T) {
	ctx := harness(t, remoteProgram(callerSource, calleeSource))

	for _, f := range ctx.UnitFiles["storefront"] {
		if f == "nomnom/pricing.py" {
			t.Errorf("the caller bundles the callee's module; the remote boundary "+
				"did not cut the import closure: %v", ctx.UnitFiles["storefront"])
		}
	}
	// And the callee still has its own entrypoint, which the same barrier
	// would remove if it were applied to the unit it names.
	found := false
	for _, f := range ctx.UnitFiles["pricing"] {
		if f == "nomnom/pricing.py" {
			found = true
		}
	}
	if !found {
		t.Errorf("the callee lost its own entry module: %v", ctx.UnitFiles["pricing"])
	}
}

// A remote call is a network round trip. A synchronous signature is the one
// thing that cannot be corrected afterwards, because by then every caller has
// been written to block on it.
func TestASynchronousRemoteFunctionIsRejected(t *testing.T) {
	callee := strings.Replace(calleeSource, "async def quote", "def quote", 1)
	ctx := compileExpectingErrors(t, remoteProgram(callerSource, callee))

	if !containsDiag(ctx, "must be declared") || !containsDiag(ctx, "async def") {
		t.Errorf("expected an error about async def, got: %v", diagStrings(ctx))
	}
}

func TestCallingAFunctionTheOtherUnitDoesNotHaveIsRejected(t *testing.T) {
	caller := strings.Replace(callerSource, "pricing.quote(items)", "pricing.quotes(items)", 1)
	ctx := compileExpectingErrors(t, remoteProgram(caller, calleeSource))

	if !containsDiag(ctx, "has no function quotes()") {
		t.Errorf("expected an error naming the missing function: %v", diagStrings(ctx))
	}
	// The message has to say what is on offer, or the author is left guessing
	// at a spelling in a file they may not have open.
	if !containsDiag(ctx, `"quote"`) {
		t.Errorf("the error should list the functions the unit offers: %v", diagStrings(ctx))
	}
	// A private helper is not part of the interface, so it is not offered.
	if containsDiag(ctx, "_internal") {
		t.Errorf("underscore names should not be offered over the wire: %v", diagStrings(ctx))
	}
}

func TestCallingAUnitThatDoesNotExistIsRejected(t *testing.T) {
	caller := strings.Replace(callerSource, `id="pricing")`, `id="prices")`, 1)
	ctx := compileExpectingErrors(t, remoteProgram(caller, calleeSource))

	if !containsDiag(ctx, "there is no execution unit") {
		t.Errorf("expected an error about the unknown unit: %v", diagStrings(ctx))
	}
	if !containsDiag(ctx, `"pricing"`) {
		t.Errorf("the error should list the units that do exist: %v", diagStrings(ctx))
	}
}

// Two units awaiting each other do not run slowly, they deadlock: each holds a
// concurrency slot open until it times out. It is also the failure ordinary
// testing misses, because the cycle only closes when both branches are taken in
// one request.
func TestMutualRemoteCallsAreRejected(t *testing.T) {
	callee := calleeSource + `
from nomnom import storefront

storefront = cloudcc.remote(storefront, id="storefront")


async def recheck(items):
    return await storefront.order(items)
`
	files := remoteProgram(callerSource, callee)
	files["nomnom/storefront.py"] = "async def order(items):\n    return items\n"
	ctx := compileExpectingErrors(t, files)

	if !containsDiag(ctx, "cycle") {
		t.Errorf("expected a cycle error: %v", diagStrings(ctx))
	}
}

func TestAUnitCannotDeclareItselfRemote(t *testing.T) {
	caller := `import cloudcompiler as cloudcc

from nomnom import pricing

cloudcc.execution_unit(id="pricing")

pricing = cloudcc.remote(pricing, id="pricing")
`
	files := map[string]string{
		"nomnom/__init__.py": "",
		"nomnom/pricing.py":  "async def quote(items):\n    return items\n",
		"app.py":             caller,
	}
	ctx := compileExpectingErrors(t, files)

	if !containsDiag(ctx, "declares itself remote") {
		t.Errorf("expected a self-call error: %v", diagStrings(ctx))
	}
}

// The caller needs two things from the stack: the callee's function name, and
// permission to invoke it. Both are derived from the calls edge.
//
// The unit ids here are chosen so the *caller* resolves first. Resolution
// walks intents in sorted order, and building a unit's policy while expanding
// it meant a caller sorted before its callee saw a callee that did not exist
// yet: no invoke permission, no function name, and an AccessDeniedException at
// runtime -- from a program that worked when the same two units were renamed.
// That is why unit wiring is a pass of its own, and this is the test that says
// so.
func TestTheCallerIsWiredToACalleeThatResolvesAfterIt(t *testing.T) {
	callee := strings.Replace(calleeSource, `id="pricing"`, `id="zulu"`, 1)
	caller := strings.NewReplacer(
		`id="storefront"`, `id="alpha"`,
		`id="pricing"`, `id="zulu"`,
	).Replace(callerSource)

	files := remoteProgram(caller, callee)
	ctx := harness(t, files)
	if ctx.Diags.HasErrors() {
		t.Fatalf("diagnostics: %v", diagStrings(ctx))
	}

	index, err := afero.ReadFile(ctx.Out, "index.ts")
	if err != nil {
		t.Fatal(err)
	}
	got := string(index)

	// In the caller's own environment block, not merely somewhere in the file:
	// every binding is also exported as a stack output, so a whole-file search
	// would pass even when the caller was handed nothing.
	_, afterEnv, hasEnv := strings.Cut(got, "const alphaEnv")
	if !hasEnv {
		t.Fatalf("the caller has no environment block:\n%s", got)
	}
	env, _, hasFn := strings.Cut(afterEnv, "const alphaFn")
	if !hasFn {
		t.Fatalf("no function was generated for the caller:\n%s", got)
	}
	if !strings.Contains(env, "CLOUDCC_UNIT_ZULU_FUNCTION") {
		t.Errorf("the caller's environment does not carry the callee's function name:\n%s", got)
	}
	if !strings.Contains(got, "lambda:InvokeFunction") {
		t.Errorf("the caller was granted no permission to invoke the callee:\n%s", got)
	}
	// Narrowly, on that function alone -- not a wildcard.
	if strings.Contains(got, `"lambda:InvokeFunction"`) && strings.Contains(got, `Resource: "*"`) {
		t.Errorf("invoke was granted on every function:\n%s", got)
	}
}

// compileExpectingErrors runs the chain over a program that should not compile
// and returns the context so the diagnostics can be inspected.
//
// The assertion that it was rejected at all is the load-bearing half: a check
// on the wording of a message that is never emitted passes for the wrong
// reason, which is the failure mode this whole file is guarding against.
func compileExpectingErrors(t *testing.T, files map[string]string) *compiler.Context {
	t.Helper()
	ctx := harness(t, files)
	if !ctx.Diags.HasErrors() {
		t.Fatalf("expected the program to be rejected, but it compiled cleanly")
	}
	return ctx
}

func containsDiag(ctx *compiler.Context, substr string) bool {
	for _, d := range diagStrings(ctx) {
		if strings.Contains(d, substr) {
			return true
		}
	}
	return false
}

// A module that declares two topics is one import. A unit that subscribes to
// only the second still runs the line that connects the first, because the
// shim reads a topic's ARN out of the environment when the module loads.
//
// Wiring only what a unit publishes to or subscribes from left the other topic
// without a binding, and the unit died on startup with a message about an
// environment variable. It is invisible locally, because a locally run unit is
// handed every stack output at once -- so it survives right up until the first
// deploy, which is the worst place to find it.
func TestAUnitIsWiredToEveryTopicItsModulesDeclare(t *testing.T) {
	files := map[string]string{
		"nomnom/__init__.py": "",
		"nomnom/events.py": `import cloudcompiler as cloudcc

placed = cloudcc.persist(cloudcc.Topic(), id="orderPlaced")
assigned = cloudcc.persist(cloudcc.Topic(), id="courierAssigned")
`,
		// Subscribes to one of the two, and imports the module declaring both.
		"tracking.py": `import cloudcompiler as cloudcc

from nomnom.events import assigned

cloudcc.execution_unit(id="tracking")


def on_assigned(message):
    return message


assigned.subscribe(on_assigned)
`,
	}
	ctx := harness(t, files)
	if ctx.Diags.HasErrors() {
		t.Fatalf("diagnostics: %v", diagStrings(ctx))
	}

	index, err := afero.ReadFile(ctx.Out, "index.ts")
	if err != nil {
		t.Fatal(err)
	}
	_, afterEnv, ok := strings.Cut(string(index), "const trackingEnv")
	if !ok {
		t.Fatalf("no environment block for the unit:\n%s", index)
	}
	env, _, _ := strings.Cut(afterEnv, "};")

	for _, want := range []string{"CLOUDCC_TOPIC_COURIERASSIGNED_ARN", "CLOUDCC_TOPIC_ORDERPLACED_ARN"} {
		if !strings.Contains(env, want) {
			t.Errorf("the unit's environment is missing %s, so importing the module "+
				"that declares it will fail at startup:\n%s", want, env)
		}
	}

	// Holding a handle is not publishing. The permission still comes from an
	// edge that says the code actually publishes.
	if strings.Contains(string(index), "sns:Publish") {
		t.Errorf("a unit that only subscribes was granted publish:\n%s", index)
	}
}

// Across languages there is nothing to pass to remote(): one process cannot
// import both a Python module and a JavaScript one, so the uncompiled program
// -- which is the whole reason a remote call is written as an ordinary call --
// could not run. A topic is the answer, and it needs no shared module.
func TestACallBetweenLanguagesIsRejected(t *testing.T) {
	files := map[string]string{
		"nomnom/__init__.py": "",
		"nomnom/pricing.py": `import cloudcompiler as cloudcc

cloudcc.execution_unit(id="pricing")


async def quote(items):
    return items
`,
		"api.js": `import * as pricingModule from "./nomnom/pricing.py";
import { executionUnit, remote } from "@cloudcompiler/sdk";

executionUnit({ id: "api" });

const pricing = remote(pricingModule, { id: "pricing" });
export { pricing };
`,
		"package.json": `{"name": "x", "type": "module"}`,
	}
	ctx := compileExpectingErrors(t, files)

	if !containsDiag(ctx, "cannot import the module it would be calling") {
		t.Errorf("expected an error about the language boundary: %v", diagStrings(ctx))
	}
	if !containsDiag(ctx, "topic") {
		t.Errorf("the error should name the thing that does work: %v", diagStrings(ctx))
	}
}

// A call over the wire is an invocation, and only a function has one.
//
// This compiled cleanly before: the callee resolved to an ECS service, no
// CLOUDCC_UNIT_<ID>_FUNCTION binding was emitted, and the caller was left to
// die on its first call with a message about an unset environment variable --
// a green deploy and an application broken on the path the seam exists for.
func TestCallingAContainerIsRejected(t *testing.T) {
	ctx := compileSourceWithConfig(t, map[string]string{
		"callee.py": `import cloudcompiler as cloudcc
cloudcc.execution_unit(id="callee")

async def work(x: int) -> int:
    return x + 1
`,
		"caller.py": `import cloudcompiler as cloudcc
import callee

cloudcc.execution_unit(id="caller")
callee = cloudcc.remote(callee, id="callee")

async def go():
    return await callee.work(1)
`,
	}, `app: t
provider: aws
execution_units:
  caller: {type: function}
  callee: {type: container}
`)

	if !ctx.Diags.HasErrors() {
		t.Fatal("a container has no invoke API, so remote() pointed at one must not compile")
	}
	msg := ctx.Diags.Items()[0].Message
	for _, want := range []string{"callee", "container", "function", "Topic"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the message should mention %q:\n%s", want, msg)
		}
	}
}

// The same program with the callee as a function is fine, so the check is
// about the compute type and not about the call.
func TestCallingAFunctionIsFine(t *testing.T) {
	ctx := compileSourceWithConfig(t, map[string]string{
		"callee.py": `import cloudcompiler as cloudcc
cloudcc.execution_unit(id="callee")

async def work(x: int) -> int:
    return x + 1
`,
		"caller.py": `import cloudcompiler as cloudcc
import callee

cloudcc.execution_unit(id="caller")
callee = cloudcc.remote(callee, id="callee")

async def go():
    return await callee.work(1)
`,
	}, `app: t
provider: aws
execution_units:
  caller: {type: function}
  callee: {type: function}
`)

	if ctx.Diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", ctx.Diags.Items())
	}
}
