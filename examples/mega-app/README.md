# mega-app

**A specification written as a program.** It uses every library category
cloudcc should support, the way it should support them, and says in comments
what the shim would do. Most of it does not compile today, and that is the
point: this is the target, written out in enough detail to argue with.

`examples/kitchen-sink` is the counterpart that *does* compile. If you want to
see what works now, read that one.

## How to read it

Start with `mega/` — one module per category, each declaring its resources —
then the four units that use them. Every proposed mapping is written as a
`shim:` comment next to the declaration it describes, and the ones with an
unresolved design question say so under `open question:`.

| File | Category | Libraries |
|---|---|---|
| `mega/orm.py` | ORMs | SQLAlchemy, Django ORM, SQLModel, Peewee, Tortoise |
| `mega/drivers.py` | DB drivers | psycopg, PyMySQL, mysqlclient, asyncpg, sqlite3 |
| `mega/nosql.py` | Other stores | redis-py, PyMongo, boto3 DynamoDB, Cassandra, ClickHouse |
| `mega/storage.py` | Files | pathlib |
| `mega/messaging.py` | Pub/sub | `cloudcc.Topic`, Celery, Pika, aiokafka, kombu |
| `mega/jobs.py` | Task queues | Celery, RQ, Dramatiq |
| `mega/caching.py` | Caching | redis-py, cachetools, dogpile.cache |
| `mega/wire.py` | Serialization | Pydantic, Marshmallow, msgspec |
| `mega/settings.py` | Configuration | python-dotenv, pydantic-settings, Dynaconf |
| `mega/logs.py` | Logging | logging, structlog, loguru |
| `mega/cloudsdk.py` | AWS SDK | boto3, preconfigured |
| `api.py` | unit, Lambda | FastAPI |
| `checkout.py` | unit, Lambda | Flask |
| `admin.py` | unit, Fargate | Django |
| `worker.py` | unit, Fargate | consumers for all five brokers |
| `ops.py` | unit, task | Typer, Click, argparse |

`coverage.yaml` is the same information in machine-readable form, and
`internal/sdkdetect/coverage_test.go` checks it against the compiler in both
directions — a row claiming support must resolve, and a row claiming a library
is unsupported must not. The day someone implements one, that test fails until
the row is updated. It also checks that every `.py` file here parses.

## What the example is arguing

Six positions, each of which shows up repeatedly in the code:

**1. `persist` is synchronous and returns the library's own type.** Every
mapping in `mega/orm.py` and `mega/drivers.py` is judged against that, and the
question it reduces to for each library is *can the password be supplied late?*
asyncpg fits perfectly (it takes an async password provider); PyMySQL barely
fits (its constructor opens the socket); Sequelize, in the Node table, does not
fit at all and is unsupported for exactly this reason.

**2. Wrap the cheapest object that names the resource.** A parameters object
beats a pool, a pool beats a live connection. `pika.ConnectionParameters`
rather than `BlockingConnection`; `psycopg_pool.ConnectionPool` rather than
`psycopg.connect`; `kombu.Connection`, which is lazy by design, unchanged.

**3. The codec belongs to the channel, not the call site.** `mega/wire.py`
works through whether Pydantic, Marshmallow or msgspec can carry what goes
between units. All three can; only the first two are recoverable from the type
alone, which is why the topic is written `Topic[OrderPlaced]` and why a
marshmallow channel has to name its schema. A publisher and a subscriber
disagreeing about the format should be a compile error.

**4. The library often declares the service, not just the capability.** For
storage the library picks a capability and `cloudcc.yaml` picks the variant.
For messaging that inverts: a program written against Kafka's ordering
guarantees cannot run on SNS, and a Kafka consumer loop pins its unit to a
container.

**5. No silent substitutions.** ClickHouse has no managed AWS equivalent, so
`persist_columnar` has no default type and the error names the two real
answers. sqlite3 cannot be made durable, so `persist` refuses and points at the
three things the author might have meant. Mapping either to something
approximate produces a program that compiles, deploys, and behaves differently
— the failure this project exists to prevent.

**6. Some categories need no capability at all.** Logging, cachetools, boto3
and python-dotenv declare nothing. What they need is for the environment to be
correct before user code runs, which is the injected runtime's job, and in two
cases a *lint* — an in-process cache in a horizontally scaled unit, or a raw
boto3 call to a service the unit's role does not cover.

## What is new here beyond the current design

- **`Topic[T]`** — a typed topic whose parameter is the wire codec.
- **`cloudcc.DjangoDatabase` / `cloudcc.TortoiseConfig`** — typed objects for
  ORMs whose "client" is otherwise an untyped dict.
- **`cloudcc.location(store)`** — the physical bucket and prefix of a persisted
  store, for the AWS APIs that need to name it. Without it, code reaches for
  `S3Path.bucket`, which does not exist on the local `Path` and so breaks the
  uncompiled run.
- **`cloudcc.log_renderer()`** — the environment-appropriate final processor
  for a structlog chain, so the chain is written once instead of behind an
  `if IS_LAMBDA`.
- **`execution_unit(type="task")`** — run-to-completion units, and
  `cloudcc run ops -- backfill --since ...`, so operational commands run with
  the unit's own role instead of credentials on a laptop.
- **One client declaring two resources** — a Celery app names a broker *and* a
  result backend. Every other row in the client table is one-to-one.
- **Module resolution in detection** — `create_engine` is both SQLAlchemy's and
  SQLModel's, and `connect` belongs to three libraries. Matching the trailing
  name cannot tell them apart.
- **Configuration-named modules join the closure** — Tortoise's
  `models={"models": ["mega.async_models"]}`, Django's `ROOT_URLCONF`, Celery's
  `include=`. A module reachable only through a string is invisible to import
  analysis, and a unit that ships without it fails at startup.

## Running it

You cannot, yet. `pip install -r requirements.txt` and the uncompiled program
would run — that is the property the whole design protects — but several of the
declarations use SDK types that do not exist. Treat it as a document with a
parser.
