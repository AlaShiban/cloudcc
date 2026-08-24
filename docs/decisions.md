# Decisions taken while building

The implementation brief settled most things. This records the places where it
was silent, ambiguous, or where following it literally would have been wrong —
so the next person does not have to re-derive the reasoning.

## Deviations from the brief

**`EnvOutputs` returns bindings, not property names.** The brief types it as
`map[string]string`, mapping an environment variable to a property of the
resource. That cannot express an RDS connection URL, which is assembled from an
address, a port and a database name. It is now `map[string]EnvBinding`, where a
binding is either a property or an arbitrary expression. Marked
`// PLAN-DEVIATION:` in `internal/ir/ir.go`.

## Interpretations where the brief was open

**"Unresolvable imports warn."** Read literally, `import os` would warn, and
every compile would drown in noise. Only *relative* imports that resolve to
nothing are warned about: those are unambiguously a local mistake. A bare
`import fastapi` is an ordinary third-party dependency, not a problem.

**`compiled/cloudcc.yaml` and `.cloudcc-state.json` are written by the CLI, not a
plugin.** Both are records *of* the whole chain, so they are produced after it
finishes rather than by a stage inside it.

**`out_dir` is recorded as `.` in the emitted config.** Recording the absolute
path a compile happened to use would make otherwise identical output differ
between runs, which the golden and double-run tests depend on not happening.
The emitted file sits in the output directory, so `.` is also the truthful
value.

**Resources are `const`, not `export const`.** Exporting a resource makes every
one of its properties a stack output, which buried the handful of environment
bindings a caller actually needs under pages of provider detail. Only the
bindings and URLs are exported.

**A gateway's URL is not injected into its own unit's environment.** The
function needs the API's ARN and the API needs the function's, so wiring the
URL back in would be a cycle. It is exported as a stack output instead, which
is how the test harness and local runs get it.

**API Gateway forwards everything through one `$default` route.** FastAPI
already owns routing; duplicating discovered routes as gateway resources would
add resources without adding behaviour. The routes are still recorded in the IR
so the topology can show them.

**Container images are pushed by a second script.** `bin/package.sh` builds
them, `bin/push-images.sh` pushes them, and `cloudcc deploy` runs the second one
after `pulumi up` — because the registry it pushes to does not exist until
then.

**RDS passwords never enter an environment variable.** AWS manages the master
credential; the URL delivered to the shim carries no password, and the managed
secret's ARN is passed separately so the shim can fetch it. Putting the
password in the environment would have defeated the point of D21.

## What the program generator found

`internal/fuzz` generates idiomatic Python and checks the compiler against its
own ground truth. Seven real bugs, none of which the hand-written tests would
have reached, because each needed a shape nobody thought to write by hand:

1. **Parenthesised string literals were rejected.** Black wraps a long string
   in parentheses and splits it across lines, so running a formatter could
   break a program that compiled a moment earlier.
2. **An empty `__init__.py` was chosen as the entrypoint.** "Shallowest Python
   file wins" picks a package's empty init and produces a unit containing
   nothing. The exposed module now wins outright.
3. **Route paths in concatenated form were silently missed**, because
   `expose.go` had its own simpler string-literal reader. There is one decoder
   now — two copies of a parser always drift.
4. **An unused SDK import survived into the bundle.** The SDK is not installed
   there, so the unit died on its first import. Every Python file is rewritten
   now, not only those containing hints.
5. **Comments between call arguments were read as arguments**, because
   tree-sitter counts a comment as a named child. A hard compile error on
   entirely ordinary code.
6. **A capability id containing a dot produced an invalid Lambda statement id**,
   which AWS rejects at deploy time.
7. **A secret configuration value always compiled to `requireSecret`**, so a
   program that supplied a default still could not deploy, and the failure came
   back as Pulumi's own message with no hint about how to set the value.

Two of these — 3 and 5 — share a root cause worth naming: reading a Python
literal is one job, and it had grown two implementations. Bug 6 and the
`uniqueName` allocator share another: sanitising is lossy, so uniqueness cannot
be a property of one name in isolation.

**Physical names are allocated, not just sanitised.** `my_bucket` and
`my-bucket` both reduce to `app-my-bucket`, which would have meant two declared
stores silently sharing one bucket and each other's data. Collisions are a
property of the whole set of names in an application, so the resolver tracks
them and appends a digest when two ids would otherwise land on the same name.

## Things that turned out to matter more than expected

**A central reference-to-dependency pass.** Every `ir.Ref` inside a resource's
properties becomes a dependency edge automatically. Leaving each mapping to
remember this produced exactly the class of bug the plugin DAG was meant to
eliminate — an IAM policy declared before the table it referenced — so it is
enforced once, for everything.

**Pulumi names resources per type, not globally.** Two ECS roles for one unit
both wanted the logical name of that unit. The renderer now disambiguates
deterministically, and the ECS mapping uses distinct ids as well.

**Pulumi list outputs cannot be indexed.** `cluster.cacheNodes[0].address` is
not valid TypeScript against an `Output`. Those properties select their element
inside an `apply`.

**The two parity tests earned their keep immediately.** The Go/Python naming
test caught `sanitize.EnvVar` adding a guard character the shims did not, and
the SDK/shim signature test was verified against a deliberate mutation. Both
guard against drift that would produce a program working locally and failing
once compiled.

**Preview needs the packaged artefacts.** The generated program references each
unit's zip by path, so a preview against a tree that was never packaged fails
on a missing file rather than showing a plan.
