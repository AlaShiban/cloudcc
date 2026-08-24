# CloudCompiler (`cc`)

Write a plain Python application. Add a few hints. Get infrastructure.

```python
# app.py
from fastapi import FastAPI, HTTPException
import cloudcompiler as cc

app = FastAPI()

pets = cc.persist_kv("petsByOwner")     # -> DynamoDB
cc.expose(app, id="pet-api")            # -> API Gateway v2 + Lambda


@app.get("/pets/{pet_id}")
def get_pet(pet_id: str):
    pet = pets.get(pet_id)
    if pet is None:
        raise HTTPException(status_code=404, detail="no such pet")
    return pet
```

```console
$ cc ./app
cc: compiled petstore into compiled
```

`cc` reads those calls statically — it never imports or runs your program —
and writes a Pulumi TypeScript project next to a copy of your source with the
hints rewritten into real AWS clients. Your own tree is never modified.

```
compiled/
├── index.ts              # the infrastructure, one const per resource
├── Pulumi.yaml           # project; Pulumi.<app>.yaml is created once and kept
├── cc.yaml               # every decision this compile made, including defaults
├── topology.mmd / .dot   # architecture diagram, rendered locally
├── bin/package.sh        # installs dependencies and zips each unit
├── main/                 # your code, rewritten
│   ├── app.py            #   cc.persist_kv(...) -> _cc_kv.connect(...)
│   ├── _cc_runtime/      #   the injected clients; the only place boto3 appears
│   ├── cc_lambda_entry.py
│   └── requirements.txt  #   yours, plus what the shims need
└── .cc-state.json        # fingerprint, so `cc deploy` can refuse stale output
```

## Install

```bash
brew install go pulumi node jq graphviz awscli uv
brew install --cask docker        # or: brew install colima docker && colima start
uv python install 3.12

git clone <this repo> && cd cloudcompiler
go build -o cc ./cmd/cc
./cc doctor                       # tells you what is missing and how to get it
```

macOS and Linux only: the Python parser is tree-sitter through cgo.

## Use

```console
$ cc ./app                        # compile (the default command)
$ cc ./app --dump-ir              # print the intermediate representation
$ cc ./app --strict               # treat warnings as errors
$ cc diagram ./app --format dot   # print the architecture
$ cc deploy ./app                 # deploy to AWS
$ cc deploy ./app --preview       # show what would change
$ cc deploy ./app --destroy       # tear it down
$ cc init                         # scaffold a cc.yaml
$ cc doctor                       # check the toolchain
```

### Try it without an AWS account

`cc` deploys against a local AWS emulator with no extra configuration:

```bash
docker run -d -p 4566:4566 -v /var/run/docker.sock:/var/run/docker.sock \
  ministackorg/ministack

cc ./examples/petstore -o /tmp/petstore
cc deploy ./examples/petstore -o /tmp/petstore --stack ministack
```

`--stack ministack` points every service at `$MINISTACK_ENDPOINT` (default
`http://localhost:4566`), supplies throwaway credentials, and keeps Pulumi
state inside the output directory. Nothing needs an account.

The compiled application itself needs no change to run against it either —
every injected client honours `CC_AWS_ENDPOINT_URL`:

```bash
cd /tmp/petstore
eval "$(pulumi stack output --json \
        | jq -r 'to_entries[]|select(.key|startswith("CC_"))|"export \(.key)=\(.value|@sh)"')"

cd main
CC_AWS_ENDPOINT_URL=http://localhost:4566 \
  uv run --with fastapi --with uvicorn --with boto3 uvicorn app:app
```

## The SDK

Everything `cc` understands is a call in the `cloudcompiler` package:

| Call | Compiles to |
|---|---|
| `cc.expose(app, id=...)` | API Gateway v2 (or an ALB) |
| `cc.execution_unit(id=...)` | Lambda (or ECS Fargate) |
| `cc.persist_kv(id)` | DynamoDB |
| `cc.persist_fs(id)` | S3 |
| `cc.persist_secret(id)` | Secrets Manager |
| `cc.persist_orm(id)` | RDS Postgres |
| `cc.persist_redis(id)` | ElastiCache (or MemoryDB) |
| `cc.pubsub_topic(id)` | SNS + subscriptions |
| `cc.static_unit(id, static_files=...)` | S3 website |
| `cc.config_value(id, secret=...)` | environment variable, Pulumi stack secret when secret |
| `cc.embed_assets(pattern)` | files bundled with the declaring unit |

Two rules follow from the hints being read rather than run:

**Arguments must be literals.** `cc.persist_kv(name)` is a compile error that
points at the argument, because `cc` would have to run your program to know
what `name` is.

**Your program still runs locally.** Outside the compiler the SDK returns small
local emulations — a dict for a KV store, a directory for a bucket — so
`uvicorn app:app` works on your laptop with no cloud account. The SDK never
imports boto3; that only ever appears in the `_cc_runtime` package injected
into the compiled copy.

## Configuration

`cc.yaml` decides every type. Nothing is inferred from the shape of your code.

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
fully-resolved result — every default `cc` filled in — is written to
`compiled/cc.yaml` after each compile, so there is always a record of what was
decided.

`pulumi_params` is the escape hatch: anything you put there is deep-merged into
the generated resource's arguments.

## Multiple execution units

Mark entrypoints and `cc` splits the program along its import graph:

```python
# api.py
cc.execution_unit(id="api")
from shared.store import pets          # -> both units share one table

# worker.py
cc.execution_unit(id="worker")
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
up in the topology, and `cc` says so.

## Further reading

- [`docs/dev.md`](docs/dev.md) — how the compiler is put together, and how to
  add a capability or a resource type.
- [`docs/testing.md`](docs/testing.md) — the test pyramid and the
  capability-by-capability support matrix.
- [`docs/decisions.md`](docs/decisions.md) — where the implementation departed
  from its brief, and why.
