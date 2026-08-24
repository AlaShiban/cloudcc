# Supporting Node.js and TypeScript

A plan, grounded in what the Python implementation actually taught us.

## Where we start from

The architecture already has the seam. A survey of the tree shows the coupling
is shallow and concentrated:

| Package | Language coupling | Verdict |
|---|---|---|
| `config`, `graph`, `ir`, `diag`, `sanitize`, `topology`, `iac/*`, `deploy` | none | reuse as-is |
| `compiler` | none — `Context` is already neutral, and `Hint` carries capability, args, file, span and receiver without any Python in it | reuse as-is |
| `provider/aws` | exactly one string: `LambdaRuntime = "python3.12"` | one field, per language |
| `source` | parses `.py` with tree-sitter-python | needs a grammar per language |
| `sdkdetect` | Python imports, Python literals | needs a sibling |
| `capabilities/{imports,execunit,expose,usage,shims}` | Python imports, FastAPI decorators, `.py` filtering | needs the language behind an interface |
| `runtime/py` | rewriter, shims, Mangum entry, packaging | needs a sibling |
| `sdk/python` | the SDK | needs an npm sibling |
| `fuzz` | generates Python | the *approach* reuses; the renderer needs a sibling |

So the work is: extract a language frontend, keep Python as the only
implementation until every test is still green, then write a second one.

That ordering is not incidental. It is the same move that made the AWS provider
and the Pulumi backend replaceable, and it is what lets the second language be
additive rather than invasive.

## The seam

```go
// Frontend is everything about a source language the rest of the compiler
// does not want to know.
type Frontend interface {
    Name() string                       // "python" | "node"
    Owns(path string) bool              // .py | .js .mjs .cjs .ts .mts .cts
    Parse(*source.File) error

    Detect(*source.File, *diag.Diagnostics) []sdkdetect.Hint
    Closure(*source.Set, entry string, excluded map[string]string) ([]string, []Unresolved)
    Routes(*source.Set, unitFiles []string, appVar string) []ir.Route
    MethodCalls(*source.Set, unitFiles []string) []MethodCall
    DefaultEntrypoint(*compiler.Context) string

    Rewrite(*source.File, []sdkdetect.Hint) error
    RuntimeFiles() (map[string][]byte, error)
    Packaging(*ir.ExecUnit) (Packaging, error)   // runtime, handler, artefact, manifest
}
```

A frontend is chosen **per execution unit**, from its entrypoint's extension.
That falls out of the design for free and means a Python worker beside a Node
API is not a special case — though it is not tested until N6.

`ir.ExecUnit` gains a `Language` field. `provider/aws` reads
`Packaging.LambdaRuntime` instead of a constant. Nothing else in the resolver
or the IaC backend changes at all.

## What is genuinely harder than Python

Naming these up front, because they are where the estimate lives.

**The module-system matrix.** ESM and CJS are both idiomatic, they differ in
runtime semantics, and every code path multiplies: detection, closure,
rewriting, the injected runtime, and the Lambda entry. Mitigation: decide the
unit's module system **once**, from `package.json#type` plus the file
extension, record it on the unit, and have everything downstream key off that
single decision rather than re-deriving it.

**TypeScript needs a build step.** Python had none. The rewritten copy is not
runnable; something has to produce JavaScript. Proposal: **esbuild**, bundling
each unit's entry to `build/<unit>/index.js`. It handles TS, JS, ESM and CJS,
it is a single binary, and bundling sidesteps shipping `node_modules` in a zip
entirely — which is the part of Node Lambda packaging that usually hurts.

The cost is native dependencies, which cannot be bundled. Escape hatch: an
`externals` list in `cloudcc.yaml`, plus a clear error rather than a mysterious
runtime failure.

**Route detection across frameworks.** Python had FastAPI decorators — one
clean syntactic marker. Node has Express, Fastify, Hono and Koa. Fortunately
`app.<verb>(path, handler)` is common to Express, Fastify and Hono, so one
pattern covers three. `express.Router()` is the analogue of `APIRouter` and
gets the same warning. Fastify's `route({method, url})` object form is a second
pattern worth having. Koa's router is out of scope for v1.

**The Mangum analogue.** `serverless-http` adapts Express/Koa/Fastify to a
Lambda handler; Hono ships its own adapter. Pick `serverless-http` as the
default and special-case Hono.

## What the Python work already paid for

Seven bugs were found by generating programs. Every one of them has a Node
analogue that can be pre-empted rather than rediscovered:

| Python bug | Node analogue to write a test for **first** |
|---|---|
| Parenthesised string literals rejected | `("a")`, `` `abc` `` (template literal, no substitution), `"a" + "b"` |
| Empty `__init__.py` chosen as entrypoint | empty `index.js`, or `package.json#main` pointing at a barrel file |
| Two literal decoders drifted | write **one** JS literal decoder, exported, used by both detection and route reading |
| Unused SDK import survived into the bundle | same — rewrite *every* source file, not only hint-bearing ones |
| Comments read as arguments | tree-sitter-javascript also makes `comment` a named node |
| Id with a dot broke a Lambda statement id | already fixed centrally; Node inherits it |
| Secret with a default still blocked deploy | already fixed centrally; Node inherits it |

That table is the single biggest saving available, and it is why the generator
gets written at N2 rather than at the end.

## Validation — the same methods, applied again

Nothing new is invented here. The methods that worked get a second instance:

1. **Golden trees + double-run determinism** — new Node examples.
2. **`tsc --noEmit`** on the generated infrastructure — unchanged, already runs.
3. **Ministack L4/L5** with probe-then-skip — unchanged.
4. **Program generator with ground truth** — a Node renderer beside the Python
   one, sharing the oracle.
5. **Differential harness** — the same script, parameterised by language: run
   the program as written against the SDK's emulations, then compiled against
   the emulator, and require identical responses.
6. **SDK ↔ shim parity** — a Node version of the signature comparison.
7. **Go ↔ runtime env-name parity** — a Node version.
8. **Compile path cannot reach the network** — unchanged, covers both.
9. **Cross-language IR equivalence** *(new, and only possible now)*: generate
   one logical program, render it in **both** languages, compile both, and
   assert the intent layer is identical modulo language. This is the strongest
   available guard against the two frontends drifting, and it costs almost
   nothing once the generator has two renderers.

## Milestones

Each has a gate. No milestone starts until the previous one's gate is green.

| | Scope | Gate |
|---|---|---|
| **N0** | Extract `Frontend`; Python becomes an implementation of it. No behaviour change. | Every existing test green, goldens byte-identical, `git diff` on `testdata` empty |
| **N1** | `sdk/node` npm package: hints, local emulations, types, vitest suite | Emulation tests pass; a hand-written Node app runs under Node with no cloud account |
| **N2** | Node frontend: parse, detect, import closure. Generator renders Node. | Ground-truth oracle passes on a Node corpus; the seven pre-empted bugs each have a passing test |
| **N3** | Rewriting, injected runtime, esbuild packaging, Lambda entry | Rewritten source runs under Node; bundle produced; parity tests green |
| **N4** | expose/routes, resolve to Lambda + API Gateway v2, deploy | Ministack L4/L5 for a Node app: up, assert, drive, destroy |
| **N5** | Node differential harness + cross-language IR equivalence | Behaviour identical uncompiled vs compiled; both languages produce the same IR |
| **N6** | ECS/container units, mixed-language applications, docs, CI | Mixed app compiles and deploys; CI runs both languages |

## Out of scope for v1

`tsconfig` path aliases (warn), decorator frameworks such as NestJS, JSX/TSX,
Koa routers, deployment without a bundler, and Deno/Bun. Each is a clean "not
yet supported" error rather than a silent misbehaviour, following the existing
convention for `eks` and `cockroachdb_serverless`.

## The one thing most likely to go wrong

The module-system matrix quietly doubling the size of every frontend function.
If that starts happening, the answer is not more branches — it is to normalise
harder at the boundary: parse to a single internal representation that records
"this unit is ESM" once, and make CJS a rendering detail of the injected
runtime rather than a fact every function has to re-check.
