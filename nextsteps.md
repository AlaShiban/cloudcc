# Next steps

Where the work stands and what is left, written so a future session can pick it
up without re-deriving any of it.

**Every milestone in `docs/plan-node.md` (N0–N6) is now met, and CI is green.**

What is left is in "Smaller things noticed but not done" at the bottom, plus
whatever the next round of sweeping turns up. The generators are the tool for
that: `CLOUDCC_FUZZ_SEEDS=500 go test ./internal/fuzz -run TestSweep` and the
same for `TestNodeSweep`.

---

## 1. ~~CI does not test Node at all~~ — DONE

`.github/workflows/ci.yml` now has a **Node SDK suite** step in the `offline`
job (`npm ci && npm run build && npm test`; the build is required because the
tests import from `dist/`, not `src/`), and a **Node client shims against real
servers** step in the `integration` job.

Verified locally with the exact commands CI runs. Two things surfaced while
doing it: the lockfile still said `0.1.0` after the version bump, and
`parity.test.js` failed with a bare `ENOENT` when run outside a checkout, since
it reaches into the compiler's template tree. Both fixed — the latter now fails
with an explanation instead.

---

## 2. ~~N4 — deploy a Node app to the emulator~~ — DONE

`./tests/e2e/ministack.sh petstore-node` provisions 11 resources (DynamoDB,
Lambda, API Gateway v2) and passes every L4 and L5 assertion, including the
DynamoDB round trip, and destroy removes the table. It runs in CI beside the
Python one.

Almost all of it already worked: `expose` and route discovery resolve for Node
unchanged, which is the language seam doing its job. The only Python assumption
left in the harness was L5, which ran `uvicorn app:app`. A Node unit is now
served by a small launcher the harness writes — the same role uvicorn plays,
and equally not part of the bundle. Which directory to serve from is decided by
the manifest, because a Node unit's `build/main` holds only esbuild's bundled
`index.mjs`, which exports a Lambda handler rather than the app.

**A pre-existing harness bug surfaced doing this**, and it is worth knowing
about: both branches backgrounded a *subshell*, so `$!` was the subshell and
cleanup left the real server running. On a clean CI host that never showed,
but locally it meant the next run's health check passed against a stale server
pointed at a destroyed stack — which produces a `ResourceNotFoundException`
from deep inside a shim and looks exactly like a product bug. Both branches now
`exec`, and the harness refuses to start when port 8099 is already held.

---

## 3. ~~N5 — Node differential harness, and cross-language IR equivalence~~ — DONE

`CLOUDCC_DIFF_LANG=node ./tests/e2e/differential.sh 1 2 3` reports identical
behaviour before and after compiling, and
`TestTheTwoFrontendsProduceTheSameIntents` compiles the same application written
in both languages and compares the intent layer field by field. Both run in CI.

The equivalence test was checked against deliberate mutations -- dropping a
capability from one side, adding a route to one side -- and catches each.

### What it looked like before



**Scope:** the same uncompiled-vs-compiled comparison Python has, for Node, plus
a check that both languages produce the same IR for equivalent programs.

`tests/e2e/differential.sh` is the model and is worth reading first — the
structure (generate → run uncompiled → compile → deploy → run compiled →
compare) transfers directly. It shells out to
`go run ./internal/fuzz/cmd/genprogram`, which only emits Python, so this is
blocked on §5.

The cross-language IR check is the more interesting half: two programs, one
Python and one Node, declaring the same capabilities should produce byte-identical
IR apart from the `language` and `runtime` fields on the execution unit. If they
do not, the language seam is leaking.

---

## 4. ~~Prove the per-library Node shims actually work~~ — MOSTLY DONE

`tests/e2e/node-clients.sh` now runs all four shim modules
(`redis_ioredis.js`, `redis_node.js`, `orm_pg.js`, `orm_knex.js`) against a real
Redis and a real Postgres in Docker, asserting each connects, round-trips, and
returns a client rather than a Promise. It runs in CI.

All four load-bearing assumptions hold, now observed rather than reasoned:
ioredis is usable immediately after construction; node-redis queues commands
after an unawaited `connect()`; `pg` accepts an async password provider; Knex
accepts an async connection factory.

**It immediately found a real bug.** `orm_pg.js` handed Postgres an *empty
string* as the password whenever there was no managed secret, and Postgres
rejects that outright rather than falling back to `PGPASSWORD` or `.pgpass`.
The fix replaced `password()` with `credentials()`, which decides synchronously
between an async provider (managed secret), a literal (password in the URL), or
saying nothing at all — the distinction between an empty password and no
password turning out to matter. Both branches are covered by the script.

Still unproven: the **managed-secret** branch, which needs Secrets Manager and
so belongs with N4/ministack rather than here. And still true that no Node unit
has been *deployed* — this exercises the shims directly, not through a Lambda.

Related: **Sequelize is deliberately unsupported.** It takes the password in its
constructor with no async provider and no async connection factory, so a shim
could only return it from an async `connect()` — which would make the compiled
binding a Promise where the uncompiled one is a client. If someone wants it
back, that is the problem to solve first, not the client table entry.

---

## 5. ~~The generator only emits Python~~ — DONE

`internal/fuzz/node.go` generates Node programs planting the same ground truth,
so both languages are checked by one oracle. 23 pinned corpus seeds
(`NodeCorpusSeeds`), a shape-coverage test over client and import spellings, and
a 1000-seed sweep, all green. `genprogram -lang node` drives the differential
harness.

I had written here that "the renderer is the work; the shapes are done." That
was optimistic: `generate.go` turned out to be Python-flavoured throughout, not
language-neutral. The Node generator is therefore written beside the Python one
rather than sharing its program builder — threading both through one builder
would have changed the order the Python side draws from its rng, and that order
is what makes the twenty pinned Python seeds reproduce. What is shared is the
part that matters: the `Program`/`Expectations` types and the oracle.

---

## 6. ~~N6 — containers, mixed-language applications~~ — DONE

`examples/mixed` is a Node API beside a Python worker sharing one DynamoDB
table. It deploys and passes every L4 and L5 assertion, and runs in CI.

It compiled correctly on the first attempt, which is the language seam doing
what it was extracted to do. Three smaller things did surface:

- A Python unit's bundle carried `package.json`. Non-source files travel with
  every unit by design, but a *language manifest* is consumed by the compiler
  and regenerated per unit, so another language's is dead weight. Now excluded.
- A generated unit manifest had no `main`, so nothing in the bundle said how to
  load the unit. `petstore-node` only appeared to work because its *source*
  manifest happened to declare one. Now set from the unit's entrypoint.
- `ministack.sh` assumed the HTTP unit was called `main`. It takes the unit
  name as a second argument now.

ECS/container units for Node are done too. A Node unit configured as `ecs`
behind an ALB now generates a container entry that calls `listen()` -- the unit's
own module only *exports* the app, since a module that listened on import could
not also be wrapped for Lambda, so this is the container's counterpart to the
Lambda entry and to uvicorn. Verified by building the generated image and
running it: it serves.

Before that fix the Dockerfile ran the unit's module directly, so the image
built, started, and served nothing -- a failure that looks like a networking
problem.

---

## Suggested order

1. ~~CI Node job~~ — done
2. ~~Prove the shims run~~ — done, and it found a bug
3. ~~N4~~ — done
4. ~~N5~~ — done
5. ~~N6~~ — done

The managed-secret branch §4 leaves open is still open: `petstore-node` has no
database, so N4 did not exercise it. An example with a `persist`ed pg client
would close it.

---

## Smaller things noticed but not done

- **`execution_unit` erases to a bare `None` statement** on its own line in
  rewritten Python (visible in any compiled `worker.py`). Valid and harmless,
  but it reads like a mistake in generated output someone will eventually read.
- **Unused SDK imports survive the rewrite.** A module that declares
  `from redis import Redis` only to hand it to `persist` keeps the import. It is
  correct — the library is in `requirements.txt` because the shim needs it — but
  a reader may wonder why.
- **`Valkey` maps to the redis-py library** in `pythonClients`. Right today,
  since valkey-py is wire-compatible and redis-py works against it, but if
  someone imports the real `valkey` package the returned client will be a
  `redis.Redis`. Worth a distinct library identifier if Valkey use grows.

---

## The library roadmap: `examples/mega-app`

Added 2026-08-25. A specification written as a program: every library category
cloudcc should support -- five ORMs, five drivers, five stores, five brokers,
three task queues, three serializers, three config libraries, three loggers,
three CLI frameworks, three web frameworks and boto3 -- used the way it should
be used, with the shim contract written as a comment next to each declaration
and the unresolved design questions marked `open question:`.

It does not compile, on purpose. `examples/kitchen-sink` is the counterpart
that does.

`examples/mega-app/coverage.yaml` is the machine-readable half, and
`internal/sdkdetect/coverage_test.go` checks it against the compiler in both
directions: a row claiming support must resolve to the stated capability and
library, and a row claiming a library is unsupported must not resolve. So
implementing one fails the test until the row moves to `supported` -- the table
cannot rot. Both mutations were checked (renaming a supported library, dropping
an ambiguity flag) and each fails as intended.

Six positions the example argues, and the work each implies, are in its README.
The three findings worth carrying forward regardless of what gets built next:

- **`create_engine` is ambiguous.** SQLAlchemy and SQLModel spell it the same
  way, and `connect` is claimed by PyMySQL, mysqlclient and sqlite3 at once.
  Detection matches the trailing name, so it cannot tell them apart -- and
  returning a nearly-right client is worse than returning none. Resolving the
  import module is a prerequisite for four of the proposed rows.
- **A module named only by a configuration string is invisible.** Tortoise's
  `models=["mega.async_models"]`, Django's `ROOT_URLCONF`, Celery's `include=`.
  Import-graph analysis will not find them, and the unit fails at startup
  rather than at compile time.
- **The codec has to live on the channel.** Pydantic, Marshmallow and msgspec
  can all carry what goes between units, but only the first two are recoverable
  from the type alone -- which is the argument for `Topic[T]`, and for making a
  publisher/subscriber format mismatch a compile error.
