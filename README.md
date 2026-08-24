# CloudCompiler (`cloudcc`)

Write a plain Python application. Add a few hints. Get infrastructure.

```python
# app.py
from fastapi import FastAPI, HTTPException
import cloudcompiler as cloudcc

app = FastAPI()

pets = cloudcc.persist(cloudcc.KVStore(), id="petsByOwner")  # -> DynamoDB
cloudcc.expose(app, id="pet-api")                            # -> API Gateway v2 + Lambda


@app.get("/pets/{pet_id}")
def get_pet(pet_id: str):
    pet = pets.get(pet_id)
    if pet is None:
        raise HTTPException(status_code=404, detail="no such pet")
    return pet
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
├── index.ts              # the infrastructure, one const per resource
├── Pulumi.yaml           # project; Pulumi.<app>.yaml is created once and kept
├── cloudcc.yaml               # every decision this compile made, including defaults
├── topology.mmd / .dot   # architecture diagram, rendered locally
├── bin/package.sh        # installs dependencies and zips each unit
├── main/                 # your code, rewritten
│   ├── app.py            #   cloudcc.persist(...) -> _cloudcc_kv.connect(...)
│   ├── _cloudcc_runtime/      #   the injected clients; the only place boto3 appears
│   ├── cloudcc_lambda_entry.py
│   └── requirements.txt  #   yours, plus what the shims need
└── .cloudcc-state.json        # fingerprint, so `cloudcc deploy` can refuse stale output
```

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
cloudcc deploy ./examples/petstore -o /tmp/petstore --stack ministack
```

`--stack ministack` points every service at `$MINISTACK_ENDPOINT` (default
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

## The SDK

Everything `cloudcc` understands is a call in the `cloudcompiler` package:

| Call | Compiles to |
|---|---|
| `cloudcc.expose(app, id=...)` | API Gateway v2 (or an ALB) |
| `cloudcc.execution_unit(id=...)` | Lambda (or ECS Fargate) |
| `cloudcc.persist(client, id=...)` | see below — the client decides |
| `cloudcc.static_unit(id, static_files=...)` | S3 website |
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
| `sqlalchemy.create_engine("postgresql…")` | RDS Postgres |
| `sqlalchemy.create_engine("mysql…")` | RDS MySQL |
| `pathlib.Path(...)` | S3, as a `cloudpathlib.S3Path` |
| `cloudcc.KVStore()` | DynamoDB |
| `cloudcc.Topic()` | SNS + subscriptions |
| `cloudcc.Secret()` | Secrets Manager |

`persist` is **type-preserving**: it returns exactly what you gave it. Uncompiled
it *is* the object you passed — your program talks to a local Redis, a local
Postgres, a local directory. Compiled, the same expression becomes a client of
the same type pointed at AWS. There is no parallel API to learn and none for us
to keep in step with yours.

Where the ecosystem has no standard client — a key/value store, a topic, a
secret — the SDK supplies a typed one, wrapped by the same verb.

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
never enter a compute bundle at all. See `examples/petstore-multi`.

## What is not here

No auth, telemetry or self-update. No Go, C# or JavaScript source. No GCP or
Azure — an unknown `--provider` is a clean error, never a silent fallback. No
EKS or CockroachDB: they are accepted by the schema and rejected at compile
time with "not yet supported", rather than quietly becoming something else. No
watch mode, no Windows.

Routes registered on a FastAPI `APIRouter` are not discovered. They are still
served — the gateway forwards everything to your unit — but they will not show
up in the topology, and `cloudcc` says so.

## Further reading

- [`docs/dev.md`](docs/dev.md) — how the compiler is put together, and how to
  add a capability or a resource type.
- [`docs/testing.md`](docs/testing.md) — the test pyramid and the
  capability-by-capability support matrix.
- [`docs/decisions.md`](docs/decisions.md) — where the implementation departed
  from its brief, and why.

## License

Apache-2.0. See [LICENSE](LICENSE).
