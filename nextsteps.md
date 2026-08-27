# Next steps

Where the work stands and what is left, written so a future session can pick it
up without re-deriving any of it.

**Every milestone in `docs/plan-node.md` (N0–N6) is met, and CI is green.**
Since then: units can call each other, there is a load test that checks the
wiring carries traffic, and one example exercises the breadth of capabilities
against real engines. See "Rounds since N6" below.

What is left is in "Smaller things noticed but not done" at the bottom, plus
whatever the next round of sweeping turns up. The generators are the tool for
that: `CLOUDCC_FUZZ_SEEDS=500 go test ./internal/fuzz -run TestSweep` and the
same for `TestNodeSweep`.

---

## Rounds since N6

**`cloudcc.remote`: units call each other.** `remote(module, id=…)` returns the
module, so uncompiled the whole application still runs in one process; compiled,
the import is removed and the await is a Lambda invocation. The compiler
enforces `async def`, that the function exists, and that the calls form no
cycle. Both runtimes implement it and share one JSON envelope, pinned by a
parity test. A call is same-language by design — one process cannot import both
a Python module and a JavaScript one — and topics cover the other case.
`examples/nomnom` is six units using both mechanisms.

**A load test that asks whether the wiring carries traffic.**
`tests/e2e/load.sh` runs a derived plan against the example as written and
against the deployed compilation, reports the ratio, and then checks every
runtime edge in the IR for evidence that something crossed it. The plan comes
from `--dump-ir`; only request bodies are supplied, from the scenario files.
Edges land in three states — carried, dead, or unverified *with the reason* —
and only `dead` fails a run. `internal/loadtest` is a separate binary and is
kept off the compile path by the test that keeps `internal/deploy` off it.

**Breadth in a deployed example.** `examples/petstore-multi` now declares ten
capability kinds: two Lambdas, API Gateway, DynamoDB, RDS Postgres, ElastiCache
Redis, Secrets Manager, an S3 bucket, an S3 website, SNS and a config value.
Least privilege falls out of the import graph — the api imports the catalogue
and gets the database and cache, the worker imports the signing module and gets
the secret, neither has the other's.

RDS and ElastiCache are no longer "preview only": the emulator provisions both
and simply runs no engine behind them, so the engines are real containers that
a scenario declares (`"engines": ["postgres", "redis"]`). Fixing three
list-valued outputs that threw on an empty list was what made RDS deployable to
an emulator at all.

### Things about the emulator worth knowing before debugging it

- It runs Lambda on **Alpine/musl**, aarch64 on an Apple Silicon host, with
  **Python 3.13 regardless of the runtime the function declares**. Bundles are
  packaged for the *target*, and the harness asks the emulator what that is
  rather than assuming.
- It reports resource addresses **on its own container network** — localhost on
  a desktop Docker that publishes ports, a container IP on CI. Both engine
  bindings are redirected for that reason; only the host and port are replaced.
- It starts a Lambda container per SNS delivery and **never reaps them**, so it
  is OOM-killed at a few hundred deliveries. This is not a memory problem: 2
  GiB, 6 GiB and 10 GiB all died at the same point. A scenario caps the scale
  its own fan-out survives. The untried alternative is reconfiguring the
  emulator for container reuse.
- `Exited (137)` on the emulator container is that kill. A run that loses the
  emulator is reported as *void* rather than failed, because an edge called
  dead when the emulator has died sends someone looking for a bug in their
  program.

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

~~The managed-secret branch §4 leaves open is still open: `petstore-node` has no
database, so N4 did not exercise it. An example with a `persist`ed pg client
would close it.~~ — closed. `examples/mixed` now declares a `pg` Pool, and the
compiled run fetches the managed credential through `orm_url.js`'s async
provider against a real Postgres.

---

## Round: client-library breadth in the examples

The examples used seven of the fifteen client libraries the compiler can
detect. Every relational and cache path in them was synchronous, no example
declared a Node database or cache at all, and the Node ORM shims were verified
only by `tests/e2e/node-clients.sh` — which proves a shim can connect, not that
a program using one survives a compile and a deploy.

**What changed.** `examples/mixed` grew from one store to four: a `pg` Pool, an
`ioredis` client, an `S3Client` and the DynamoDB client it already had, with
`worker.py` declaring three of the same four ids through a SQLAlchemy engine, a
`pathlib.Path` and a boto3 Table. One Postgres instance, reached through a
connection pool from Node and an ORM from Python. `examples/petstore-multi`
became the asynchronous half of the pair: its api unit declares
`create_async_engine` and `redis.asyncio.Redis`, and its worker declares
`create_engine` against a second database — one application whose two Lambdas
hold different client libraries for the same capability. `docs/testing.md` has
the library-to-example table.

**Four defects found, each on the real environment.**

1. **`redis.asyncio.Redis` compiled to the synchronous client.** Detection
   reduced a constructor to its final attribute, so both Redis libraries were
   "Redis"; `LibRedisPyAsync` was declared and never produced, and
   `redis_.py`'s `connect` had no `library` argument. A program awaiting a
   compiled cache call would have got `TypeError: object bool can't be used in
   'await' expression`. Fixed by resolving the constructor's full dotted path
   through the file's imports (`qualifiedConstructor` + `clientOrigins`) and
   refining the client in `sdkdetect.RefineClient`. `redis-py-async` also had
   no `ShimRequirements` entry, so its bundle would have shipped without redis
   at all; a test now requires an entry for every library the compiler can name.

2. **The load generator measured itself.** Go's default HTTP client keeps two
   idle connections per host, so above two sessions in flight nearly every
   request opened a fresh socket — and against localhost that exhausts the
   ephemeral port range in seconds. `examples/mixed` produced 2.2 million
   "connection refused" results and a table of reassuring 1.00x ratios, because
   both halves hit the same wall. Fixed in `newClient`, with a test that counts
   connections per request and fails at 0.35.

3. **A run that delivered nothing reported every edge as carried.** The
   connectedness checklist reads state out of the emulator, and state outlives
   the run that wrote it — so a run whose requests never arrived found a
   previous run's rows and passed. `Report.Undelivered()` now voids a run below
   a 50% delivered rate, and both `-attach` and `-compare` refuse it.

4. **Two identical failures passed the differential suite.** An async ORM
   session that expires attributes on commit raised `MissingGreenlet` on every
   write in *both* halves, so the diff matched and `examples.sh` reported the
   example green. It now fails any run containing a 5xx, whether or not the two
   halves agree on it. Proven by reintroducing the bug.

Two smaller ones, both in the harness: buckets were created but never emptied,
unlike tables, so the uncompiled half read objects a previous run had left
(`ensure_local_bucket`); and Postgres databases came from one `POSTGRES_DB` on
first container start, so an application declaring two could not be tested
(`scenario_databases`, driven by the scenario).

**What the examples now show that they did not.** `create_async_engine` needs
`+asyncpg` spelled in the source URL: uncompiled there is no shim to swap the
driver, and SQLAlchemy refuses a bare `postgresql://`. And Express 4 does not
catch a rejected promise from an async handler — Node exits the process — so
`examples/mixed` wraps its handlers, which is what killed its first load run.

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

---

## The review round: five decisions implemented

Added 2026-08-25, after a review of `examples/mega-app`. Five of the eight
decisions were removals from the example; three changed the product.

### 1. No SDK objects for data stores

`cloudcc.KVStore` and Node's `KVStore`/`FileStore` are gone. A store is
declared by wrapping the client library you already use: a boto3 `Table`, a
`DynamoDBClient`, an `S3Client`. The shims return the library's own object.

The parity list is two pairs long now instead of five, and every pair removed
is a pair that can no longer drift. A boto3 `Table` names one table, so the
Python shim binds it; a `DynamoDBClient` names none, so the Node shim installs
a middleware that rewrites whatever name the program wrote to the provisioned
one. `tests/e2e/node-clients.sh` proves that against a real DynamoDB and S3,
and the assertion was checked against a deliberate break.

**The cost, stated plainly:** an uncompiled run now needs a real endpoint for
each store. That was already true of Redis and Postgres; the key/value store
was the exception, and buying that exception cost a class, a local file format
and a parity test. `differential.sh` creates the local table in the emulator
and points both halves at it with `AWS_ENDPOINT_URL`, which is the AWS SDKs'
own variable -- so the program as written needs no cloudcc-specific
configuration to reach it.

### 2. A log destination, with one value and a seam

`logging.type` in cloudcc.yaml. `cloudwatch` works; `datadog` and `honeycomb`
are recognised and refused. Logging is the only capability declared purely in
configuration, so nothing in the source mentions it -- which is why it needed a
validation path of its own and a ministack assertion of its own. Both exist.

The vendor seam is the destination, not the call sites: a Datadog integration
is a different handler installed by `configure()` and nothing else changes.

### 3. A topic declares its guarantees; the compiler picks the service

`Topic(subscribers=…, ordering=…, delivery=…, replay=…, retention_hours=…,
max_message_kb=…)`. The selector resolves those to SNS, SQS, either FIFO form
or Kinesis, and refuses a set no service can meet with the constraint to relax.
A type in cloudcc.yaml is checked against the requirements rather than obeyed,
because unlike ElastiCache-vs-MemoryDB these variants behave differently.

**Only SNS is provisioned.** Selecting one of the other four is a clean error
naming the service and the requirement that forced it. That is the honest
position and the obvious next piece of work: SQS is the cheapest of the four to
add, and it needs a queue resource, an event-source mapping, and a runtime
dispatch branch for the SQS envelope.

### Still open

- **Only SNS is provisioned** of the five topic backings the selector can
  reach. SQS is the cheapest to add: a queue, an event-source mapping, and an
  SQS-envelope branch in the runtime dispatch.
- **A secret's edge is permanently unverified** in the load test. Reading one
  leaves no observable trace; where its value is used for something observable,
  that write is the evidence under its own edge.
- **The load test cannot tell a read-only store from a dead write path.** The
  emulator serves no read counters. Using the uncompiled run as a control would
  settle it and is the obvious next improvement.
- **`create_engine("postgresql+pg8000://…")` silently becomes psycopg2 once
  compiled.** The provider builds the URL scheme itself and drops the driver
  the program asked for. Nothing has needed it yet, but it is a silent
  substitution, which this project does not otherwise allow.

### Smaller things this round turned up

- `ministack.sh` hardcoded `uvicorn app:app`, so it could only run an example
  whose entry was `app.py`. It asks the compiler which module holds the
  application now, and `petstore-multi` joined CI as a result.
- A test that loaded a Python shim by path left a `__pycache__` **inside the
  embedded template tree**, so that `.pyc` would have shipped in every bundle.
  The golden trees caught it. `RuntimeFiles` refuses to ship bytecode now.
- Both "the SDK never imports an AWS client" tests grepped source text, so they
  failed the moment the documentation had to name boto3. They read imports now.

- Four calls to methods that did not exist survived in the examples --
  `audit.write(...)`, `docs.write(...)` twice and `docs.list()` -- all left over
  from when the SDK supplied a `FileStore` class. Three were in an example that
  cannot deploy and the fourth in a handler nothing invoked. A test now asks
  Python whether each method exists on the type `persist` returned, and the
  emulator harness waits for the subscriber to write to its own store.
- The harness pointed eleven AWS services at the emulator and `cloudcc deploy`
  pointed at nineteen, so the availability-zone lookup went to real AWS and
  failed with `AuthFailure`. One list now, pinned by a test.
- A missing shell function is a runtime lookup that `bash -n` cannot see, so a
  block edit that removed three helpers from `lib.sh` cost three separate
  eight-minute deploys to find. `tests/e2e/lib_surface_test.go` checks the
  harness surface in `make check`.
