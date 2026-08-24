# Working on cloudcc

## Setting up

```bash
brew install go pulumi node jq graphviz awscli uv
brew install --cask docker        # or: brew install colima docker && colima start
uv python install 3.12

go build -o cloudcc ./cmd/cloudcc && ./cloudcc doctor
```

`cloudcc doctor` is the check: it names every tool it wants, says what each is for,
and prints the exact `brew` command for anything missing. Required tools make
it exit non-zero; optional ones are reported and shrugged off.

macOS and Linux only. The Python parser is tree-sitter through cgo — there is
no mature pure-Go Python parser, which is the same trade Klotho 1 made.

### The AWS emulator

Integration tests run against [ministack](https://github.com/ministackorg/ministack),
an AWS emulator in a container:

```bash
docker run -d -p 4566:4566 -v /var/run/docker.sock:/var/run/docker.sock \
  ministackorg/ministack
```

The Docker socket mount is what lets it back Lambda, RDS and ECS with real
processes rather than mocks. The endpoint is read from `$MINISTACK_ENDPOINT`
everywhere and never hardcoded, so LocalStack or a remote emulator can be
substituted by setting one variable:

```bash
MINISTACK_ENDPOINT=http://localhost:4567 ./tests/e2e/ministack.sh
```

## Architecture

```
source/ + cloudcc.yaml
      |
      v
config -> input -> detect -> static-units -> embed-assets -> exec-units
                                                                  |
       expose / persist / pubsub / config-vars <-------------------+
                          |
                       validate
                          |
                        shims  ------------------> compiled/<unit>/
                          |
                     resolve:aws
                          |
              topology + render:pulumi-ts -------> compiled/index.ts
```

One process, one pass, no server, no network. Every stage mutates a shared
`compiler.Context`.

### The parts that carry weight

**Plugins declare their dependencies; the schedule is derived.** Klotho 1
ordered its plugins by hand, which made the ordering both load-bearing and
invisible. Here each plugin names what must run before it and Kahn's algorithm
produces the order, with an alphabetical tie-break so it is deterministic. A
cycle or an unknown dependency fails at schedule time, not as a mysterious
runtime bug.

The one ordering that is genuinely subtle is `static-units` before
`exec-units`: static assets must be claimed out of the file pool while it is
still whole, or they get swallowed into compute bundles.

**Two IR layers, connected by `resolves_to` edges.** Capability plugins create
*intents* — "a KV store named petsByOwner". `internal/provider/aws` is the only
thing that creates *resources* — "an aws.dynamodb.Table". Keeping them separate
is what makes another provider possible without touching capability code, and
`--dump-ir` shows both layers plus the expansion between them. Do not let a
capability plugin reach for a resource.

**Generation is data, not code.** A concrete resource is a template id, a
property map and a set of environment bindings. `internal/iac/pulumi_ts` has a
registry of template rows and one renderer. Klotho 1 hand-wrote a TypeScript
module per resource type, so supporting a new resource meant writing new
TypeScript; here it means adding a row.

**Output is byte-deterministic.** Every map iteration is sorted, nothing is
timestamped, no id is random. Golden tests and the deploy fingerprint both
depend on it, and a double-run diff test enforces it.

## Adding things

### A new resource type

1. Add a row to `templates` in `internal/iac/pulumi_ts/registry.go` — the
   Pulumi class, the module, and the suffix its generated variable gets.
2. Emit it from `internal/provider/aws`, using `ir.Ref` to point at other
   resources rather than writing TypeScript.
3. If it needs a VPC, call `r.network()`; if it is a managed datastore, call
   `r.subnetGroup(...)`.

You should not have to touch the renderer. If you do, that is worth a second
look.

Two things the resolver does for you, so no mapping has to remember them:
every `ir.Ref` inside a resource's properties becomes a dependency edge, and
two resources of the same Pulumi class can never end up with the same logical
name.

### A new capability

1. Add the function to `sdk/python/cloudcompiler/__init__.py` with a local
   emulation in `_emulation.py`.
2. Add its signature to `signatures` in `internal/sdkdetect/hint.go`.
3. Add an intent type to `internal/ir/intents.go` and a plugin under
   `internal/capabilities` that creates it, plus a row in
   `internal/capabilities/chain.go`.
4. Add a runtime client under `internal/runtime/py/templates/_cloudcc_runtime/`
   whose public methods match the SDK emulation exactly, and a rewrite rule in
   `internal/runtime/py/rewrite.go`.
5. Map it in `internal/provider/aws`, and add it to `typeSupport`.
6. Add it to `examples/kitchen-sink` and regenerate the golden trees.

Steps 1 and 4 are two implementations of one API, which is why
`sdk/python/tests/test_shim_parity.py` compares them method by method. Steps 2
and 5 are two spellings of one environment variable name, which is why
`internal/provider/aws/parity_test.go` compares those. Both tests caught real
drift while they were being written.

## Expression vocabulary

The resolver never writes TypeScript. It produces property values from four
types in `internal/ir/expr.go`:

| Type | Renders as |
|---|---|
| `ir.Ref{Key, Prop}` | `someResource.prop` |
| `ir.Lit(parts...)` | `pulumi.interpolate\`...\`` |
| `ir.JSONDoc{Value}` | `pulumi.jsonStringify({...})` |
| `ir.Raw("...")` | verbatim |

`Raw` is the escape hatch and every use of it is a place the output stops being
backend-neutral. There are only a handful, and they are commented.

A Pulumi list output cannot be indexed directly, so a `Prop` that needs an
element uses an apply: `Prop: "cacheNodes.apply(n => n[0].address)"`.

## Deployment is isolated

`internal/deploy` is the only package allowed to touch the network, and nothing
on the compile path imports it. That keeps the Automation API's considerable
weight out of `cloudcc compile` and makes "compilation is offline" checkable rather
than aspirational — `TestTheCompilePathCannotReachTheNetwork` and
`TestDeployIsIsolated` in `internal/capabilities/offline_test.go` enforce both
halves.

## Conventions

- Every generated artefact says it was generated and that edits will be lost.
- Diagnostics accumulate, capped at 50, sorted by position, formatted
  `file:line:col: severity: capability: message`. A pass that aborts on the
  first problem forces a round-trip per mistake.
- Warnings become errors under `--strict`.
- Deviations from the implementation brief are marked `// PLAN-DEVIATION:` with
  the reason.
