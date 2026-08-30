package capabilities

import (
	"fmt"
	"sort"
	"strings"

	"github.com/cloudcompiler/cloudcc/internal/compiler"
	"github.com/cloudcompiler/cloudcc/internal/config"
	"github.com/cloudcompiler/cloudcc/internal/graph"
	"github.com/cloudcompiler/cloudcc/internal/ir"
	"github.com/cloudcompiler/cloudcc/internal/lang"
	"github.com/cloudcompiler/cloudcc/internal/sdkdetect"
)

// RemotePlugin turns cloudcc.remote hints into calls edges between execution
// units, and checks the three things that stop being true once an in-process
// call becomes a request.
//
// The edge is the whole resource story: the callee already exists in the graph
// as an execution unit, so nothing new is provisioned. What the edge earns the
// caller is the callee's function name in its environment and permission to
// invoke it -- both derived, like every other permission, from an edge rather
// than from a list somebody maintains.
//
// The checks are the reason this is a compiler feature rather than a library.
// A library can offer `await other.thing()`; only something that reads both
// sides can say at compile time that `thing` exists, that it is `async def`,
// and that the two units are not waiting on each other.
type RemotePlugin struct{ base }

// NewRemotePlugin returns the remote stage. It runs after exec-units because
// every check it makes is about a unit that must already be in the graph.
func NewRemotePlugin() *RemotePlugin {
	return &RemotePlugin{base{name: PluginRemote, deps: []string{PluginExecUnits}}}
}

func (p *RemotePlugin) Transform(ctx *compiler.Context) error {
	for _, h := range ctx.HintsFor(sdkdetect.CapRemote) {
		p.declaration(ctx, h)
	}
	p.rejectCycles(ctx)
	return nil
}

// declaration handles one remote() call.
func (p *RemotePlugin) declaration(ctx *compiler.Context, h sdkdetect.Hint) {
	target := h.ID()
	calleeKey := ir.Key{Kind: config.KindExecutionUnit, ID: target}
	callee, ok := ctx.Graph.Intent(calleeKey)
	if !ok {
		ctx.Diags.Errorf(ctx.HintPos(h), sdkdetect.CapRemote,
			"there is no execution unit %q to call; this program declares %s",
			target, quotedList(unitIDs(ctx)))
		return
	}

	// A call over the wire is an *invocation*, and only a function has one.
	//
	// A container is reached at an address, by something that already knows the
	// address; there is no API that says "run this and give me the answer". So
	// a remote() pointed at one compiled cleanly, emitted no function binding,
	// and left the caller to die on its first call with a message about an
	// environment variable -- a green deploy and a broken application, which is
	// the failure this check exists to move to compile time.
	if unit, isUnit := callee.(*ir.ExecUnit); isUnit {
		if typ := unit.Config().Type; typ != "" && typ != config.TypeFunction {
			ctx.Diags.Errorf(ctx.HintPos(h), sdkdetect.CapRemote,
				"execution unit %q is `type: %s`, and only `type: %s` can be called over the wire: "+
					"a call is an invocation, and a container is reached at an address instead. "+
					"Make %q a function, or send it a message with a Topic -- which is what the "+
					"other direction of this seam is for",
				target, typ, config.TypeFunction, target)
			return
		}
	}

	callers := ctx.UnitsFor(h.File)
	if len(callers) == 0 {
		ctx.Diags.Warnf(ctx.HintPos(h), sdkdetect.CapRemote,
			"%q is declared remote in a file no execution unit imports, so nothing will call it", target)
		return
	}

	// The handle is how a call site is recognised. Without one there is nothing
	// to match `handle.method(...)` against, so neither the check below nor the
	// call itself can happen.
	if h.Receives == "" {
		ctx.Diags.Warnf(ctx.HintPos(h), sdkdetect.CapRemote,
			"the result of remote(id=%q) is not assigned to a variable, "+
				"so no call to it can be found", target)
	}

	for _, caller := range callers {
		if caller == target {
			ctx.Diags.Errorf(ctx.HintPos(h), sdkdetect.CapRemote,
				"execution unit %q declares itself remote; a unit calls its own "+
					"functions directly, and invoking itself would wait on a second "+
					"copy of the process that is already running", target)
			continue
		}
		if !p.languagesSupported(ctx, h, caller, target) {
			continue
		}
		p.checkCalls(ctx, h, caller, callee.(*ir.ExecUnit))

		callerKey := ir.Key{Kind: config.KindExecutionUnit, ID: caller}
		ctx.Graph.Connect(callerKey, calleeKey, ir.EdgeCalls)
		// The uses edge is what environment wiring and IAM are derived from,
		// everywhere in this compiler. The calls edge says what kind of use it
		// is; this one says that it is one.
		ctx.Graph.Connect(callerKey, calleeKey, ir.EdgeUses)
	}
}

// languagesSupported reports whether a call between these two units can be
// compiled, and explains it when not.
//
// Two conditions, and they fail for different reasons.
//
// A runtime must be able to answer an inbound call at all. That is a
// dispatcher in the generated entrypoint, and a language without one would
// compile cleanly and then reply to every call with a shape its caller cannot
// read.
//
// The two units must also be written in the *same* language, and that one is a
// property of the design rather than something left to build. What makes a
// remote call worth writing is that uncompiled it is an ordinary call:
//
//	from nomnom import pricing
//	pricing = cloudcc.remote(pricing, id="pricing")
//
// The argument is the callee's module, imported the ordinary way, and one
// process cannot import both a Python module and a JavaScript one. Across
// languages there would be nothing to pass and nothing to type-check against,
// and the program would only run once compiled -- which is the property this
// compiler exists to avoid depending on. Units in different languages talk
// through a topic, which is a message rather than a call and needs no shared
// module; examples/mixed does exactly that.
func (p *RemotePlugin) languagesSupported(ctx *compiler.Context, h sdkdetect.Hint, caller, callee string) bool {
	for _, unit := range []struct{ id, role string }{{caller, "calls"}, {callee, "is called"}} {
		front, ok := ctx.Frontend(unit.id)
		if !ok {
			continue
		}
		if _, supported := front.RemoteFunctions(ctx.Files, ""); !supported {
			ctx.Diags.Errorf(ctx.HintPos(h), sdkdetect.CapRemote,
				"execution unit %q %s over the wire, but nothing in the %s runtime "+
					"dispatches an inbound call yet. A topic carries messages between "+
					"units in any language", unit.id, unit.role, front.Name())
			return false
		}
	}

	callerFront, okA := ctx.Frontend(caller)
	calleeFront, okB := ctx.Frontend(callee)
	if okA && okB && callerFront.Name() != calleeFront.Name() {
		ctx.Diags.Errorf(ctx.HintPos(h), sdkdetect.CapRemote,
			"execution unit %q is written in %s and %q in %s, so %q cannot import "+
				"the module it would be calling. A remote call is an ordinary call "+
				"until it is compiled, and that is what makes it worth writing; across "+
				"languages there is nothing to pass to remote() and nothing to check "+
				"the call against. Send a message through a topic instead, which needs "+
				"no shared module",
			caller, callerFront.Name(), callee, calleeFront.Name(), caller)
		return false
	}
	return true
}

// checkCalls validates every `handle.method(...)` site in the calling unit
// against the functions the callee actually offers.
//
// This is the check that pays for the whole feature. A misspelled remote call
// is otherwise an AttributeError inside whichever request first takes that
// branch, in a different service, discovered in production; here it is a
// compile error next to the call.
func (p *RemotePlugin) checkCalls(ctx *compiler.Context, h sdkdetect.Hint, caller string, callee *ir.ExecUnit) {
	if h.Receives == "" {
		return
	}
	front, ok := ctx.Frontend(caller)
	if !ok {
		return
	}
	calleeFront, ok := ctx.Frontend(callee.ID)
	if !ok {
		return
	}

	offered := map[string]lang.RemoteFunction{}
	var names []string
	for _, entry := range callee.Entrypoints {
		fns, _ := calleeFront.RemoteFunctions(ctx.Files, entry)
		for _, fn := range fns {
			if _, dup := offered[fn.Name]; dup {
				continue
			}
			offered[fn.Name] = fn
			names = append(names, fn.Name)
		}
	}
	sort.Strings(names)

	for _, call := range front.MethodCalls(ctx.Files, ctx.UnitFiles[caller]) {
		if call.Object != h.Receives {
			continue
		}
		fn, exists := offered[call.Method]
		if !exists {
			ctx.Diags.Errorf(ctx.Pos(call.File, call.Offset), sdkdetect.CapRemote,
				"execution unit %q has no function %s(); it offers %s",
				callee.ID, call.Method, quotedList(names))
			continue
		}
		if !fn.Async {
			// The requirement is not stylistic. Compiled, this call is a
			// network round trip, and a synchronous signature is the one thing
			// that cannot be made true afterwards: every caller has already
			// been written to block on it, so making it async later is a
			// breaking change to all of them. Requiring `async def` up front
			// costs one keyword and keeps the option open.
			ctx.Diags.Errorf(ctx.Pos(call.File, call.Offset), sdkdetect.CapRemote,
				"%s.%s() is called over the wire, so it must be declared "+
					"`async def` in %s. A remote call is a network round trip, and a "+
					"signature that hides one behind a blocking call cannot be corrected "+
					"later without changing every caller",
				h.Receives, call.Method, strings.Join(callee.Entrypoints, ", "))
		}
	}
}

// rejectCycles refuses a program in which units await each other.
//
// Two units calling each other synchronously is not slow, it is stuck: each
// invocation holds its caller open, and on Lambda both sit there until the
// timeout, having consumed two concurrency slots to make no progress. Under
// load that exhausts the account's concurrency and takes down services that
// have nothing to do with either of them.
//
// It is also the one failure here that testing does not reliably find, because
// a cycle only closes when both branches are taken in the same request.
func (p *RemotePlugin) rejectCycles(ctx *compiler.Context) {
	g := graph.New[string]()
	for _, in := range ctx.Graph.IntentsOfKind(config.KindExecutionUnit) {
		g.Add(in.Key().ID, in.Key().ID)
	}
	for _, e := range ctx.Graph.Edges() {
		if e.Kind == ir.EdgeCalls {
			g.Connect(e.From.ID, e.To.ID)
		}
	}
	if _, err := g.TopoSort(); err == nil {
		return
	}

	// The hint positions are where the author can act, so every remote()
	// declaration involved in the program is reported rather than an abstract
	// statement about the graph.
	var edges []string
	for _, e := range ctx.Graph.Edges() {
		if e.Kind == ir.EdgeCalls {
			edges = append(edges, e.From.ID+" -> "+e.To.ID)
		}
	}
	sort.Strings(edges)
	for _, h := range ctx.HintsFor(sdkdetect.CapRemote) {
		ctx.Diags.Errorf(ctx.HintPos(h), sdkdetect.CapRemote,
			"these remote calls form a cycle (%s). Two units awaiting each other "+
				"deadlock until both time out, holding a concurrency slot each. "+
				"Break it by making one direction a topic, which does not wait",
			strings.Join(edges, ", "))
		return
	}
}

// unitIDs returns every execution unit in the program, sorted.
func unitIDs(ctx *compiler.Context) []string {
	var out []string
	for _, in := range ctx.Graph.IntentsOfKind(config.KindExecutionUnit) {
		out = append(out, in.Key().ID)
	}
	sort.Strings(out)
	return out
}

// quotedList renders names for a diagnostic, or says plainly that there are
// none -- an empty "it offers " is the kind of message that sends someone
// looking for a bug in their spelling.
func quotedList(names []string) string {
	if len(names) == 0 {
		return "no functions at all"
	}
	quoted := make([]string, 0, len(names))
	for _, n := range names {
		quoted = append(quoted, fmt.Sprintf("%q", n))
	}
	return strings.Join(quoted, ", ")
}
