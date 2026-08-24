# Decisions taken while building

The implementation brief settled most things. This records the places where it
was silent, ambiguous, or where following it literally would have been wrong —
so the next person does not have to re-derive the reasoning.

## Changes made after the brief

**One `persist` verb, and the wrapped client decides the capability.** The brief
specified a verb per store — `persist_kv`, `persist_redis`, `persist_orm` and so
on — each returning an SDK object. That made the SDK a second implementation of
every client library, and the SDK/shim parity test existed only to slow the
resulting drift. It could not fix it: a program calling `cache.expire(...)`
would need us to have anticipated `expire`.

The surface is now `persist(client, *, id)`, which returns exactly the client it
was given:

```python
cache = cloudcc.persist(Redis(host="localhost"), id="itemCache")
db    = cloudcc.persist(create_engine("postgresql://localhost/shop"), id="shopdb")
```

The type of the argument is what the compiler reads to decide the resource —
`internal/sdkdetect/client.go` holds that table — and the injected shim returns
a client of that same type pointed at AWS. The parity problem disappears rather
than being managed, because there is only ever one implementation: the library's
own.

**The client library, not just the capability, decides what is built.** The
capability alone is not enough information. Two Redis libraries have different
APIs; a synchronous SQLAlchemy engine is not an asynchronous one; `pg` and Knex
share nothing. `sdkdetect.Client.Library` records which one the program reached
for, it travels on the intent, and the shim dispatches on it:

- Python keeps one module per capability and branches inside it, because its
  imports are lazy and only the taken branch ever executes. `create_async_engine`
  gets an `AsyncEngine` back, with the driver swapped to an async one.
- Node has one module per *library*, each importing exactly one package. That
  is what lets the bundler pull in ioredis or node-redis but never both, and it
  keeps `connect` synchronous — a single static import needs no await.

**`connect` is synchronous everywhere it returns a client.** Uncompiled,
`persist(new Redis(), …)` hands back a client immediately. An async `connect`
would make the same expression a client before compiling and a Promise after,
so `cache.get(k)` would stop working. Every supported library connects lazily,
and the managed database password is passed as an async *provider* — `pg` and
Knex both accept one — rather than being awaited up front. A Node test asserts
no shim declares `connect` async.

Sequelize is deliberately unsupported for exactly this reason: it takes the
password in its constructor with no async provider and no async connection
factory, so a shim could only return it from an async `connect`. An
unrecognised client is a compile error naming what *is* supported, which beats
a bundle that fails on its first query.

Three consequences worth naming:

- **Ids are always explicit**, never inferred from the variable. Renaming a
  local would otherwise replace a live resource.
- **The client is the weakest layer of configuration.** A Redis client asks for
  ElastiCache, but an explicit `cloudcc.yaml` entry still wins, so choosing
  MemoryDB stays a configuration change. `App.HasExplicitType` is the guard.
- **Python has no `FileStore` class; Node does.** `pathlib.Path` already is one
  and `cloudpathlib.S3Path` mirrors it, so Python wraps the real thing. Node's
  `fs` is a set of functions with no object to wrap, so there a class is
  unavoidable — and it is one of the few that still needs the parity test. The
  asymmetry reflects the ecosystems, not an oversight.

This was caught by the parity test itself: a `FileStore` added to the Python SDK
for symmetry had `read`/`write`/`list`, while the shim returned an `S3Path` that
has none of them. It would have compiled and then failed at runtime on the first
call — exactly the failure the redesign exists to prevent.

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
