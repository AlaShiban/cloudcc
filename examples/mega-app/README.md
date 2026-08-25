# mega-app

**A specification written as a program.** It uses every library category
cloudcc should support, the way it should support them, and says in comments
what the shim would do. Some of it does not compile today, and that is the
point: this is the target, written out in enough detail to argue with.

It has been through one round of review, and five things were cut rather than
implemented. They are worth reading as decisions, not omissions: the reasoning
is at the bottom of the file each one used to live in.

`examples/kitchen-sink` is the counterpart that *does* compile. If you want to
see what works now, read that one.

## How to read it

Start with `mega/` — one module per category, each declaring its resources —
then the four units that use them. Every proposed mapping is written as a
`shim:` comment next to the declaration it describes, and the ones with an
unresolved design question say so under `open question:`.

| File | Category | Libraries |
|---|---|---|
| `mega/orm.py` | ORMs | SQLAlchemy, SQLModel, Peewee, Tortoise |
| `mega/drivers.py` | DB drivers | psycopg, PyMySQL, mysqlclient, asyncpg |
| `mega/nosql.py` | Other stores | redis-py, boto3 DynamoDB, PyMongo |
| `mega/storage.py` | Files | pathlib |
| `mega/messaging.py` | Pub/sub | `cloudcc.Topic`, Celery, Pika, aiokafka, kombu |
| `mega/jobs.py` | Task queues | Celery, RQ, Dramatiq |
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

**0. cloudcc supplies no objects for data stores.** A store is declared by
wrapping the client library you would have used anyway — a `boto3` DynamoDB
`Table`, a `redis.Redis`, a `pathlib.Path`. A supplied class is a dialect
nobody else speaks, its methods have to be kept in step with the shim's
forever, and it makes the code unliftable. The cost is that an uncompiled run
needs a real endpoint for its stores, which was already true of every store
except the one this rule removed.

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

**4. A topic declares its requirements, and the compiler picks the service.**
For a store, the library picks the capability and `cloudcc.yaml` picks the
variant. A topic has no library to ask, and the variants are not
interchangeable — SNS cannot replay, SQS cannot fan out, FIFO costs throughput.
So `Topic[T]` carries the decisions that change its behaviour (subscribers,
ordering, delivery, replay, retention, size) and the compiler resolves them to
SNS, SQS, their FIFO forms or Kinesis — or fails naming the constraint to
relax. Reaching for aiokafka or pika is how you take that decision back.

**5. No silent substitutions.** ClickHouse has no managed AWS equivalent, so
`persist_columnar` has no default type and the error names the two real
answers. sqlite3 cannot be made durable, so `persist` refuses and points at the
three things the author might have meant. Mapping either to something
approximate produces a program that compiles, deploys, and behaves differently
— the failure this project exists to prevent.

**6. Some categories need no client, only a correct environment.** boto3 and
python-dotenv declare nothing at all: what they need is for the environment to
be right before user code runs, which is the injected runtime's job. Logging is
the near miss — no client to declare, but a *destination* to choose, so it gets
a `logging.type` in `cloudcc.yaml` with `cloudwatch` as the only working value
and vendors refused rather than ignored.

## What was cut in review, and why

- **`cloudcc.KVStore`** — the last SDK-supplied data store. Replaced by a
  `boto3` DynamoDB `Table`. See the top of `mega/nosql.py`.
- **The Django ORM** — synchronous by default, and queries issued on attribute
  access, so the blocking call is invisible at the call site. Django stays as a
  web framework. See the bottom of `mega/orm.py`.
- **`sqlite3` persistence** — there is no correct cloud resource for it.
- **Cassandra and ClickHouse** — Keyspaces is compatible in the way DocumentDB
  is compatible, and ClickHouse has no managed AWS service at all. A capability
  whose local emulation and deployed resource differ in ways the differential
  harness cannot catch is worse than no capability. See the bottom of
  `mega/nosql.py`.
- **The whole caching category** — `redis-py` already covers it, `cachetools`
  is a dict with an eviction policy, and `dogpile.cache` would have meant a
  second way to declare the cluster that already exists.

## What is new here beyond the current design

- **`Topic[T](...)`** — a typed topic that carries its own requirements, whose
  type parameter is the wire codec.
- **`cloudcc.TortoiseConfig`** — a typed object for an ORM whose "client" is
  otherwise an untyped dict.
- **`cloudcc.location(store)`** — the physical bucket and prefix of a persisted
  store, for the AWS APIs that need to name it. Without it, code reaches for
  `S3Path.bucket`, which does not exist on the local `Path` and so breaks the
  uncompiled run.
- **`cloudcc.log_renderer()`** — the final processor for a structlog chain that
  matches the configured destination, so the chain is written once instead of
  behind an `if IS_LAMBDA`, and a vendor integration has one place to reach.
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

Not yet, in one piece. `pip install -r requirements.txt` and most of it would
run uncompiled — that is the property the whole design protects — but several
declarations still use SDK types that do not exist, and the stores now need
real local endpoints (a DynamoDB, a Redis, a Postgres, a Mongo) because they
hold real clients. Treat it as a document with a parser.
