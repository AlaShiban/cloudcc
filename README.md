# CloudCompiler (`cloudcc`)

Write a plain application — Python, TypeScript or JavaScript. Add a few hints.
Get infrastructure.

```python
# app.py
import json

import boto3
from fastapi import FastAPI, HTTPException

import cloudcompiler as cloudcc

app = FastAPI()

table = boto3.resource("dynamodb").Table("pets")
pets = cloudcc.persist(table, id="petsByOwner")  # -> DynamoDB
cloudcc.expose(app, id="pet-api")                # -> API Gateway v2 + Lambda


@app.get("/pets/{pet_id}")
def get_pet(pet_id: str):
    item = pets.get_item(Key={"id": pet_id}).get("Item")
    if item is None:
        raise HTTPException(status_code=404, detail="no such pet")
    return json.loads(item["pet"])
```

```console
$ cloudcc ./app
cloudcc: compiled petstore into compiled
```

`cloudcc` reads those calls statically — it never imports or runs your program —
and writes a Pulumi TypeScript project next to a copy of your source with the
hints rewritten into real AWS clients. Your own tree is never modified.

```
compiled/
└── petstore/                 # one folder per application, so out_dir is shareable
    ├── index.ts              # the infrastructure, one const per resource
    ├── Pulumi.yaml           # project; Pulumi.<app>.yaml is created once and kept
    ├── cloudcc.yaml          # every decision this compile made, including defaults
    ├── topology.mmd / .dot   # what the program declared -- the capability layer
    ├── architecture.mmd/.dot # what it compiled to -- every resource that will exist
    ├── architecture.py       # the same, as a `diagrams` program, with AWS icons
    ├── bin/package.sh        # installs dependencies and zips each unit
    ├── main/                 # your code, rewritten
    │   ├── app.py            #   cloudcc.persist(...) -> _cloudcc_kv.connect(...)
    │   ├── _cloudcc_runtime/ #   the injected clients; the only place boto3 appears
    │   ├── cloudcc_lambda_entry.py
    │   └── requirements.txt  #   yours, plus what the shims need
    └── .cloudcc-state.json   # fingerprint, so `cloudcc deploy` can refuse stale output
```

`out_dir` holds **a folder per application**. `compiled/` with one app's
`index.ts` in it is fine until a second app is compiled beside it, at which
point the two overwrite each other and the first anyone hears of it is a deploy
that replaces the wrong stack.

## Install

```bash
brew install go pulumi node jq graphviz awscli uv
brew install --cask docker        # or: brew install colima docker && colima start
uv python install 3.12

git clone <this repo> && cd cloudcompiler
go build -o cloudcc ./cmd/cloudcc
./cloudcc doctor                       # tells you what is missing and how to get it
```

macOS and Linux only: the Python parser is tree-sitter through cgo.

## Use

```console
$ cloudcc ./app                        # compile (the default command)
$ cloudcc ./app --dump-ir              # print the intermediate representation
$ cloudcc ./app --strict               # treat warnings as errors
$ cloudcc diagram ./app --format dot   # print the architecture
$ cloudcc deploy ./app                 # deploy to AWS
$ cloudcc deploy ./app --preview       # show what would change
$ cloudcc deploy ./app --destroy       # tear it down
$ cloudcc init                         # scaffold a cloudcc.yaml
$ cloudcc doctor                       # check the toolchain
```

### Try it without an AWS account

`cloudcc` deploys against a local AWS emulator with no extra configuration:

```bash
docker run -d -p 4566:4566 -v /var/run/docker.sock:/var/run/docker.sock \
  ministackorg/ministack

cloudcc ./examples/petstore -o /tmp/petstore
cloudcc deploy ./examples/petstore -o /tmp/petstore --stack local
```

`--stack local` points every service at `$CLOUDCC_EMULATOR_ENDPOINT` (default
`http://localhost:4566`), supplies throwaway credentials, and keeps Pulumi
state inside the output directory. Nothing needs an account.

The compiled application itself needs no change to run against it either —
every injected client honours `CLOUDCC_AWS_ENDPOINT_URL`:

```bash
cd /tmp/petstore
eval "$(pulumi stack output --json \
        | jq -r 'to_entries[]|select(.key|startswith("CLOUDCC_"))|"export \(.key)=\(.value|@sh)"')"

cd main
CLOUDCC_AWS_ENDPOINT_URL=http://localhost:4566 \
  uv run --with fastapi --with uvicorn --with boto3 uvicorn app:app
```

### Seeing what it actually did

A compiled unit talks to resources you did not name, through bindings you did
not write. When it misbehaves, the useful evidence is the sequence of calls it
made at its stores — not a stack trace, and not a log line somebody remembered
to add.

Set `CLOUDCC_TRACE=1` on either half and every call through a persisted client
is written to stderr, tagged, so it survives a Lambda (where it reaches
CloudWatch) and interleaves harmlessly with your own logging:

```bash
CLOUDCC_TRACE=1 uvicorn app:app 2> trace.log
cloudcc trace trace.log
```
```
== petsByOwner (kv)
   put_item args={"kw":{"Item":{"id":"1","pet":"…"}}} ret={}
   get_item args={"kw":{"Key":{"id":"1"}}} ret={"Item":{"id":"1","pet":"…"}}
```

Resources appear under the id *you* gave them in `persist(id=...)`, never the
provisioned name — which is what lets a local run and a deployed one be
compared directly:

```bash
cloudcc trace before.log --diff after.log     # non-zero if they did different work
```

Tracing is off unless the variable is set. `persist` still hands back the
library's own object, so nothing about your program changes when it is unset.

## The SDK

Everything `cloudcc` understands is a call in the `cloudcompiler` package. This
section is the reference; [Every capability, by
example](#every-capability-by-example) is the same ground in one snippet each,
with [a TypeScript mirror](docs/typescript.md).

| Call | Compiles to |
|---|---|
| `cloudcc.expose(app, id=...)` | API Gateway v2 (or an ALB) |
| `cloudcc.execution_unit(id=...)` | Lambda (or ECS Fargate) |
| `cloudcc.persist(client, id=...)` | see below — the client decides |
| `cloudcc.static_unit(id, static_files=...)` | S3 website (or CloudFront) |
| `cloudcc.config_value(id, secret=...)` | environment variable, Pulumi stack secret when secret |
| `cloudcc.embed_assets(pattern)` | files bundled with the declaring unit |

### `persist` — bring your own client

There is one verb for state, and what you hand it is what decides the resource:

```python
cache = cloudcc.persist(Redis(host="localhost"), id="itemCache")
db    = cloudcc.persist(create_engine("postgresql://localhost/shop"), id="shopdb")
docs  = cloudcc.persist(Path("./itemDocs"), id="itemDocs")
```

| What you pass | Compiles to |
|---|---|
| `redis.Redis(...)` | ElastiCache (or MemoryDB) |
| `sqlalchemy.create_async_engine(...)` | RDS, as an `AsyncEngine` |
| `sqlalchemy.create_engine("postgresql…")` | RDS Postgres |
| `sqlalchemy.create_engine("mysql…")` | RDS MySQL |
| `pathlib.Path(...)` | S3, as a `cloudpathlib.S3Path` |
| `boto3.resource("dynamodb").Table(...)` | DynamoDB, as a `Table` |
| `cloudcc.Topic()` | SNS, or SQS for a single subscriber |
| `cloudcc.Secret()` | Secrets Manager |

A topic is the one place where the arguments, not the client, decide the
service — because the variants do not behave alike:

```python
events = cloudcc.persist(cloudcc.Topic(subscribers="one"), id="petEvents")
```

`subscribers="many"` is a fan-out and compiles to SNS, which pushes each
notification to every subscriber. `subscribers="one"` is a work queue and
compiles to SQS, which the subscriber's function polls through an event source
mapping. Both ends of your program are unchanged — `publish()` and
`subscribe()` mean the same thing either way — and the queue's visibility
timeout is derived from how long the subscriber may run, because AWS refuses a
mapping where the function can outrun it.

`ordering`, `replay` and the rest still resolve to FIFO queues, FIFO topics and
Kinesis, and those three are refused at compile time rather than approximated.

### Static units: a bucket, or a bucket behind a CDN

```yaml
static_units:
  petstore-site:
    type: cloudfront      # s3 | cloudfront
```

`s3` serves the objects from the bucket's own website endpoint. `cloudfront`
keeps the bucket private and puts a distribution in front of it, reaching the
objects through an origin access identity and no other way — one address for
the content instead of two, and the cached one at that. The cost is that the
index document only applies at the root: CloudFront does not rewrite `/docs/`
to `/docs/index.html` the way an S3 website does, which is why these are two
types rather than a flag.

Where logs go is configuration rather than code:

```yaml
logging:
  type: cloudwatch     # the only destination implemented
  retention_days: 14
```

`datadog` and `honeycomb` are recognised and refused rather than ignored, so
choosing one is a clear error instead of a key that silently does nothing. The
seam for a vendor is the destination, not the call sites: nothing in an
application changes when this does, which is the property that makes the
integration worth routing through a compiler at all.

`persist` is **type-preserving**: it returns exactly what you gave it. Uncompiled
it *is* the object you passed — your program talks to a local Redis, a local
Postgres, a local directory. Compiled, the same expression becomes a client of
the same type pointed at AWS. There is no parallel API to learn and none for us
to keep in step with yours.

**The SDK supplies no data-store classes.** A store is declared by wrapping the
library you already use, because a class of ours would be a dialect nobody else
speaks and its methods would have to be kept in step with the injected
runtime's forever. The two things it does supply — a topic and a secret — are
not stores: neither has a client to wrap.

The cost is that an uncompiled run needs a real endpoint for each store: a local
Redis, a local Postgres, a local DynamoDB. That was already true of everything
except the key/value store, which has stopped being the exception.

Two rules follow from the hints being read rather than run:

**Arguments must be literals.** `cloudcc.persist(client, id=name)` is a compile
error that points at the argument, because `cloudcc` would have to run your
program to know what `name` is.

**Ids are always explicit.** They are deliberately not taken from the variable
you assign to: renaming a local would otherwise replace a live resource, and
losing a database to a rename is not a trade worth making for brevity.

**Your program still runs locally.** `uvicorn app:app` works on your laptop with
no cloud account. The SDK never imports boto3; that only ever appears in the
`_cloudcc_runtime` package injected into the compiled copy.

A TypeScript program needs something that can run TypeScript, exactly as a
Python one needs uvicorn:

```bash
npx tsx server.ts        # or: node --import tsx server.ts
```

`tsx` is esbuild underneath, which is also what packages the unit, so the
program you run and the program you deploy are transformed the same way.
Node's own `--experimental-strip-types` is not enough here: it is
erasable-syntax-only, so enums, namespaces and constructor parameter properties
fail, and it does not resolve `./store.js` to `store.ts` — a spelling `cloudcc`
does support. Nothing `cloudcc` *produces* needs `tsx`: a unit is bundled by
esbuild on the way out, so what gets deployed is plain JavaScript either way.

## Every capability, by example

Python below. **[The same gallery in TypeScript →](docs/typescript.md)** — same
capabilities, same resources, only the spelling differs.

Each snippet is distilled from a working example in `examples/`, and every one
of those is deployed and compared before and after compiling on each run.

```python
import cloudcompiler as cloudcc
```

### An execution unit

```python
cloudcc.execution_unit(id="api")
```

→ a Lambda. `type: container` in `cloudcc.yaml` makes it ECS Fargate;
`platform: kubernetes` makes it a Deployment on EKS. The code does not change —
that is the whole point of the axis being configuration.

### Exposing HTTP

```python
app = FastAPI()
cloudcc.expose(app, id="pet-api")
```

→ API Gateway v2 in front of the unit, or an ALB for a container unit.
`cloudcc` only needs to know *which variable* holds the app; your framework
still owns routing.

### Key/value — DynamoDB

```python
catalogue = cloudcc.persist(boto3.resource("dynamodb").Table("catalogue"), id="catalogue")

catalogue.put_item(Item={"id": "1", "name": "rex"})
```

→ a DynamoDB table. You keep a boto3 `Table`, so `put_item`, `query`,
`batch_writer` and the paginators all work — because they *are* boto3's.

### Relational — RDS

```python
db = cloudcc.persist(
    create_engine("postgresql://ccadmin@localhost:5432/shopdb"),
    id="shopdb",
    models=["Item", "Order"],
)
```

→ RDS Postgres; a `mysql://` URL gives RDS MySQL, and `create_async_engine`
gives an `AsyncEngine` back. The master credential is managed by AWS and never
enters an environment variable.

### Cache — ElastiCache

```python
cache = cloudcc.persist(Redis(host="localhost"), id="itemCache")

cache.setex(f"item:{item_id}", 300, summary)
```

→ ElastiCache. `type: memorydb` in `cloudcc.yaml` switches the service without
touching this line. What you hold is a `redis.Redis`, with the library's own
defaults — the compiler supplies where it connects and nothing else.

### Files — S3

```python
docs = cloudcc.persist(Path("./itemDocs"), id="itemDocs")

(docs / f"{item_id}.json").write_text(json.dumps(body))
```

→ an S3 bucket, and `docs` becomes a `cloudpathlib.S3Path`: the `/` operator,
`read_text`, `iterdir` and `exists` all behave as they did locally.

### Pub/sub — SNS, or SQS

```python
events = cloudcc.persist(cloudcc.Topic(), id="itemEvents")

events.publish({"action": "upserted", "id": item_id})    # publisher
events.subscribe(on_item_event)                          # subscriber, another unit
```

→ SNS for fan-out. `cloudcc.Topic(subscribers="one")` is a work queue and
compiles to SQS with an event-source mapping instead — the same two lines of
program either way. The queue's visibility timeout is derived from how long the
subscriber may run, because AWS refuses a mapping whose function can outrun it.

### Secrets

```python
signing_key = cloudcc.persist(cloudcc.Secret(), id="signingKey")

key = signing_key.get()
```

→ Secrets Manager. `cloudcc` provisions the secret and deliberately not its
value, so nothing sensitive lands in the generated project or the state file
(D21). Setting it is a deploy-time step, and an unset secret says so rather
than yielding `""`.

### Calling another unit

```python
from nomnom import pricing

pricing = cloudcc.remote(pricing, id="pricing")

quote = await pricing.quote_basket(items)
```

→ a Lambda invoke, with arguments and return values crossing as JSON.
Uncompiled it is an ordinary in-process call, so the program runs as one
process. The functions must be `async def`, the names must exist, and the calls
may not form a cycle — all three are compile errors rather than production
surprises.

### Configuration values

```python
log_level = cloudcc.config_value("log_level", default="info")
stripe_key = cloudcc.config_value("stripe_key", secret=True)
```

→ an environment variable on the unit; `secret=True` makes it a Pulumi stack
secret, encrypted in state rather than sitting in plaintext.

### Assets bundled with a unit

```python
SEED = cloudcc.embed_assets("./data/*.json")
```

→ the matching files travel inside the unit's own deployment bundle, and the
call returns the directory they landed in.

### A static site, optionally behind a CDN

```python
cloudcc.static_unit(
    "petstore-site",
    static_files="./public/**/*",
    index_document="index.html",
)
```

→ an S3 website. `type: cloudfront` keeps the bucket private and puts a
distribution in front of it, reachable only through an origin access identity.

### Logging

```python
logging.getLogger(__name__).info("started")
```

→ CloudWatch, with the unit's identity attached, configured before your module
is imported. Where logs go is `cloudcc.yaml`'s business rather than your
program's — which is what makes swapping the destination a change to one key
instead of to every call site.

### Seeing what it did

```bash
CLOUDCC_TRACE=1 uvicorn app:app 2> trace.log
cloudcc trace trace.log
```

→ every call the program made through a persisted client, grouped by the id you
gave it. See [above](#seeing-what-it-actually-did).

## Configuration

`cloudcc.yaml` has the final say on every type. The client your program reached
for is the weakest layer — a Redis client asks for ElastiCache — so an explicit
entry here still wins, and moving to MemoryDB stays a configuration change
rather than a code change.

```yaml
app: petstore
provider: aws

defaults:
  execution_unit: { type: lambda }        # lambda | ecs
  persist_redis:  { type: elasticache }   # elasticache | memorydb

execution_units:
  api:
    type: lambda
    environment_variables: { LOG_LEVEL: info }
  worker:
    type: ecs

persisted:
  petsByOwner:
    type: dynamodb
    pulumi_params:                        # merged into the generated resource
      billingMode: PAY_PER_REQUEST
```

Layering runs weakest to strongest: `defaults.<kind>`, then
`defaults.<kind>.by_type.<type>`, then the explicit entry for that id. The
fully-resolved result — every default `cloudcc` filled in — is written to
`compiled/cloudcc.yaml` after each compile, so there is always a record of what was
decided.

`pulumi_params` is the escape hatch: anything you put there is deep-merged into
the generated resource's arguments.

## Multiple execution units

Mark entrypoints and `cloudcc` splits the program along its import graph:

```python
# api.py
cloudcc.execution_unit(id="api")
from shared.store import pets          # -> both units share one table

# worker.py
cloudcc.execution_unit(id="worker")
from shared.store import pets
```

Each unit gets its own function, its own role and its own environment. A module
both units import is copied into both bundles. Files a static unit claimed
never enter a compute bundle at all. See `examples/petstore-multi`, or
`examples/petstore-multi-ts` for the same application in TypeScript.

### The examples

Every example exists in both languages. The Python one is the original; the
`-ts` sibling is the same application written in TypeScript, and both are
compiled, deployed and compared before and after compiling.

| | Python | TypeScript | what it is for |
|---|---|---|---|
| one unit | `petstore` | `petstore-ts` | the smallest thing that works |
| breadth | `petstore-multi` | `petstore-multi-ts` | two units, every store, a queue, a CDN |
| coverage | `kitchen-sink` | `kitchen-sink-ts` | every capability, both compute types |
| many units | `nomnom` | `nomnom-ts` | six units, remote calls and fan-out topics |
| Kubernetes | `k8s-web` | `k8s-web-ts` | a container, and nothing that names the platform |
| two languages | — | `mixed` | a TypeScript API beside a Python worker |
| JavaScript | — | `petstore-node` | plain JavaScript, no types |
| the target | `mega-app` | — | a specification; it does not compile, on purpose |

## What is not here

No auth, telemetry or self-update. No Go or C# source. No GCP or
Azure — an unknown `--provider` is a clean error, never a silent fallback. No
CockroachDB: it is accepted by the schema and rejected at compile time with
"not yet supported", rather than quietly becoming something else. No watch
mode, no Windows.

Python, JavaScript and TypeScript are the languages, and a unit's frontend is
chosen from its entrypoint's extension — so a Python worker beside a TypeScript
API is not a special case. Within TypeScript, decorator frameworks (NestJS),
Koa routers, Deno and Bun are not supported, JSX/TSX parses but implies no
framework support, and a `tsconfig` `extends` chain deeper than one level is a
clear error rather than a configuration read halfway.

Routes registered on a FastAPI `APIRouter` are not discovered. They are still
served — the gateway forwards everything to your unit — but they will not show
up in the topology, and `cloudcc` says so.

### Diagrams, every compile

Two layers, three notations, no flags. `topology` is the handful of capability
nodes the program declared — the picture to reason about. `architecture` is
every resource that will exist in the account, roles and log groups and VPC
plumbing included — the picture to review before a deploy.

| | |
|---|---|
| `topology.mmd` / `.dot` | the declared capabilities, in Mermaid and Graphviz |
| `architecture.mmd` / `.dot` | every resolved resource, in Mermaid and Graphviz |
| `architecture.py` | the *architecture*, as a [`diagrams`](https://pypi.org/project/diagrams) program — one AWS icon per capability |

Three images, each under its own name and never substituted for one another:
`<app>.png` (declared topology), `<app>-resources.png` (graphviz's render of
every resource) and `<app>-architecture.png` (the icon diagram). If `diagrams`
is not installed the last one is simply absent — which is visible. Writing the
resource graph under that name instead would leave a file that *looks* like the
architecture and is not.

```console
$ cloudcc diagram ./app --view architecture --format mermaid
$ pip install diagrams && python compiled/myapp/architecture.py
```

The two `architecture` files answer different questions, and it matters which
you reach for. The Mermaid and DOT ones are **exhaustive** — every resource that
will exist, roles and log groups and route tables included — and the e2e harness
checks them against what the stack actually created. `architecture.py` is the
**architecture**: one icon per capability, the service it resolved to, and the
edges the program itself declared. Nobody draws the execution role when they
sketch a service on a whiteboard.

It is worth having as a *file* rather than only an image: it is the one output
someone can edit — regroup it, annotate it for a review — without hand-drawing
anything, and it diffs in a pull request like the rest of the compiled tree. A
PNG is rendered during the compile when `diagrams` and graphviz are both
installed, and skipped with a one-line note when they are not. `cloudcc doctor`
reports whether the package is there, which is the difference between choosing
not to have the picture and not knowing it was available:

```console
$ pip install diagrams
$ cloudcc doctor | grep diagrams
  ok       diagrams /usr/bin/python3 (import diagrams)
``` Nothing is
downloaded to draw a picture nobody asked for.

All three are rendered from the program's own edges rather than a structure
built for drawing, so none can show something that was not compiled or omit
something that was. Rendering is entirely local (D12).

## Further reading

- [`docs/typescript.md`](docs/typescript.md) — every capability in TypeScript,
  mirroring the gallery above.
- [`docs/dev.md`](docs/dev.md) — how the compiler is put together, and how to
  add a capability or a resource type.
- [`docs/testing.md`](docs/testing.md) — the test pyramid and the
  capability-by-capability support matrix.
- [`docs/decisions.md`](docs/decisions.md) — where the implementation departed
  from its brief, and why.

## License

Apache-2.0. See [LICENSE](LICENSE).
