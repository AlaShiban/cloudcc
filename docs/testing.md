# Testing

## The pyramid

| Layer | What it checks | Network | Command |
|---|---|---|---|
| **L1 unit** | Each package in isolation: config merging, the scheduler, tree-sitter detection, name sanitising, the rewriter, diagnostics | none | `go test ./...` |
| **L2 golden** | Whole compiled trees for three examples, plus a double-run diff proving byte determinism | none | `go test ./internal/cli` |
| **L3 type/shape** | `tsc --noEmit` on the generated project under `strict` | none | inside the e2e harness |
| **L4 provisioning** | `pulumi up` against the emulator, then assert through the AWS CLI | emulator | `./tests/e2e/provisioning.sh` |
| **L5 functional** | Run the compiled application against those resources; assert HTTP responses *and* datastore state | emulator | `./tests/e2e/provisioning.sh` |
| **Python** | SDK emulations, and their signature parity with the injected shims | none | `cd sdk/python && uv run --with pytest --with-editable . python -m pytest tests` |
| **Generated corpus** | Twenty generated programs compiled and checked against the generator's own ground truth | none | `go test ./internal/fuzz` |
| **Examples** | Every example run uncompiled and compiled; every response must match, and each app's architecture diagram must match the deployed stack | emulator | `./tests/e2e/examples.sh` |
| **Differential** | The same generated program run uncompiled and compiled; every response must match | emulator | `./tests/e2e/differential.sh` |
| **Load** | Throughput before and after compiling, and whether every edge the compiler drew carried traffic | emulator | `./tests/e2e/load.sh` |

## Examples, twice

`tests/e2e/examples.sh` runs every example as written -- against real stores in
the emulator, because a program now holds a real client -- then compiles it,
deploys it, runs the compiled copy, and replays the same requests against both.
Every response must match byte for byte.

It overlaps the differential harness deliberately, and neither replaces the
other: the generator covers twenty spellings of an import and would never have
written `examples/mixed`; the examples cover the code a newcomer copies.

Every example is accounted for. `kitchen-sink` and `mega-app` cannot deploy,
and each says why out loud rather than being left out of a loop -- a suite that
silently covers four of six reads exactly like a suite that covers six.

The icon diagram gets its own check in the offline job: CI installs `diagrams`,
which turns `TestEveryMappedClassExists` from a skip into a real test of every
class name in the mapping, and then renders a compiled example for real. Both
ways that program can be wrong are silent from Go's side -- a class that does
not exist, and a call site the imports did not bind -- and each produces no
picture rather than an error.

It also checks each application's **architecture diagram** against the stack
Pulumi actually created: same services, same count. Writing that check was
worth it twice over -- the first version compared nothing at all and passed,
and the second undercounted by two because a topic's Mermaid shape opens with
`>` and the character class missed it.

```bash
go test ./...                              # L1 + L2
./tests/e2e/provisioning.sh                   # L3 + L4 + L5, through raw pulumi
./tests/e2e/deploy.sh                      # the same path through `cloudcc deploy`
cd sdk/python && uv run --with pytest --with-editable . python -m pytest tests
```

`CLOUDCC_E2E_KEEP=1` leaves the work directory and the deployed stack in place for
inspection; the harness prints the command to tear it down.

## Generated programs

`internal/fuzz` writes idiomatic Python programs that use the SDK, from a seed,
along with the ground truth of what a correct compiler must find in them. That
turns the oracle from "did it compile" into "did it find exactly what I
planted, and nothing else".

The point is coverage of *shape*. The compiler reads syntax rather than running
code, so a hint written in a form it does not recognise becomes a resource that
silently does not exist — nothing complains until production. The generator
varies:

- all four SDK import styles, including aliased from-imports;
- flat, package and nested-package layouts, with relative and absolute imports;
- one to three execution units, with and without an explicit `execution_unit`;
- string literals as single, double, triple-quoted, implicitly concatenated and
  parenthesised-across-lines;
- calls laid out on one line, across lines, with trailing commas, with spaces
  inside the parentheses, and with comments between arguments;
- bindings as plain assignment, annotated assignment, two statements on one
  line, inside an `if`, and as a class attribute;
- handlers sync and async, plain and stacked under a user decorator;
- ids containing dots, dashes, underscores, digits and mixed case;
- files with CRLF endings, tab indentation and trailing whitespace.

```bash
go test ./internal/fuzz                                   # the frozen corpus
CLOUDCC_FUZZ_SEEDS=500 go test ./internal/fuzz -run TestSweep   # hunt for more
CLOUDCC_FUZZ_SEEDS=100 CLOUDCC_FUZZ_START=9000 go test ./internal/fuzz -run TestSweep
```

A seed always reproduces the same program, so any failure reproduces from the
seed alone — and a failure prints the whole program, not just its number.
Interesting seeds get promoted into `CorpusSeeds`.

## The differential test

`tests/e2e/differential.sh` is the correctness guarantee the compiler actually
owes: that rewriting a program does not change what it does.

Each generated program is run twice — once as written, against the SDK's local
emulations, and once after compiling, against real AWS services in the
emulator — and the same request scenario is replayed against both. Every status
code and every response body must match.

```bash
./tests/e2e/differential.sh            # seeds 1 2 3
./tests/e2e/differential.sh 7 11 13
CLOUDCC_E2E_KEEP=1 ./tests/e2e/differential.sh 4   # keep the workdir to inspect
```

The scenario exercises real state transitions — a miss, a write, a hit, a
listing, a delete, a miss again — so a compiler that dropped writes or lost a
key would show up as a diff rather than as two identical empty results.

Only KV and file stores take part. A secret reads as empty locally and as its
real value in the cloud, and an ORM handle is a connection URL; those are not
observably equivalent by design, so comparing them would be comparing the
emulation, not the compiler.

## Load, and what a load test is for here

`tests/e2e/load.sh` asks two questions, and the second is the one a load test
does not usually ask.

**Throughput** is the familiar one: how much the application sustains, and at
what latency. It is measured twice -- the example as written, and the compiled
application deployed -- so the number that comes out is a *ratio*, which is what
survives being run on a different machine. Four strategies, because a single
"fire N requests" answers none of the questions well:

| Strategy | The question |
|---|---|
| `steady` | What does it sustain, with everything else held still |
| `ramp` | Where does throughput stop rising as concurrency does |
| `burst` | What does an idle system do when traffic arrives all at once |
| `drain` | How long does the asynchronous half take to catch up |

**Connectedness** is the other one. A compiled application is a graph, and the
failure this project is organised against is an edge that looks right in the
plan and is dead at runtime: a store nothing wrote to, a topic whose subscriber
never woke, a unit nobody invoked. Each of those is a green deploy and a broken
application, and none of them shows up as an error. After the load has run,
every runtime edge in the IR is checked for evidence that it carried something.

The plan is derived from the compiler's own IR, so an example that grows a
route grows this test with it. The only thing supplied by hand is request
bodies, which come from the scenario files the differential suite already uses:
no amount of reading a graph says that an order needs items in it.

An edge lands in one of three states, and the third is the honest one:

- **carried** -- evidence found: rows in the table, objects in the bucket, log
  streams for the unit.
- **dead** -- no evidence, and the unit at the edge's source did run. There was
  an opportunity and nothing took it. This fails the run.
- **unverified** -- no evidence, and the harness cannot tell why. A unit that
  only *reads* a table leaves it empty, and the emulator serves no read
  counters, so a read-only path and a dead write path look identical from
  outside. `nomnom`'s `pricing -uses-> menuPrices` is exactly this. Calling it
  dead would be a false accusation; calling it fine would be a check that
  passes without looking.

```sh
./tests/e2e/load.sh                       # every deployable example
./tests/e2e/load.sh nomnom                # one of them
CLOUDCC_LOAD_SCALE=0.25 ./tests/e2e/load.sh   # shorter, for CI
```

Reports are written to `compiled/<app>/loadtest-{uncompiled,compiled}.json` and
can be compared later with `loadgen -compare before.json after.json`.

**Load is the heaviest thing the emulator is asked to do.** An application with
six units has the emulator plus six Lambda containers resident at once, and on a
Docker VM sized for ordinary use that can exhaust it -- the container is
OOM-killed and everything after it fails unhelpfully. The harness checks that
the emulator is still answering before it draws any conclusion, and says the run
is *void* rather than failed, because an edge reported as dead when the emulator
has died would send someone looking for a bug in their program. `nomnom` needs
roughly 4-6 GB; `colima stop && colima start --memory 6` is enough.

**The ratios are against the emulator, not real AWS.** Its Lambda invoke is far
slower than the real one, which is why `nomnom` -- six units and up to three
sequential cross-unit calls per request -- reads as a much larger cost than it
would in an account. The number is worth watching for *change*; it is not a
production figure.

## Probe, then assert

Before asserting against a service, the harness makes one cheap call to it. A
non-2xx answer means the emulator does not implement it well enough to test
against, and the assertion **skips with a printed reason**:

```
 warn SKIP: the emulator at http://localhost:4566 does not answer apigatewayv2;
      this assertion was not run
```

A gap in the emulator must never read as a pass. If a row in the matrix below
starts skipping, that is a signal, not noise.

## Capability × emulator matrix

| Capability | AWS target | Provisioning (L4) | Functional (L5) |
|---|---|---|---|
| `persist(boto3…Table())` | DynamoDB | yes | yes, through the running app |
| `persist(Path(...))` | S3 | yes | yes, through the running app |
| `persist(Secret())` | Secrets Manager | yes | yes, through the running app |
| `pubsub` | SNS + subscription | yes | publish reaches the subscriber |
| `static_unit` | S3 website | yes | object fetch |
| `expose` | API Gateway v2 | probe-dependent | via local uvicorn/Mangum instead |
| `execution_unit` (lambda) | Lambda | probe-dependent | direct handler invoke |
| `execution_unit` (container, serverless) | ECS Fargate | yes | a task runs; the ALB's own address is unverified |
| `execution_unit` (container, kubernetes) | EKS | yes, a real k3s | pod runs, Service routes |
| `persist(create_engine(...))` | RDS Postgres | yes | yes, against a real Postgres in Docker |
| `persist(create_async_engine(...))` | RDS Postgres | yes | yes, against a real Postgres in Docker |
| `persist(Redis())` | ElastiCache | yes | yes, against a real Redis in Docker |
| `persist(redis.asyncio.Redis())` | ElastiCache | yes | yes, against a real Redis in Docker |
| `persist(new Pool())` (Node) | RDS Postgres | yes | yes, against a real Postgres in Docker |
| `persist(new Redis())` (Node) | ElastiCache | yes | yes, against a real Redis in Docker |
| `persist(new S3Client())` (Node) | S3 | yes | yes, through the running app |
| `persist(new DynamoDBClient())` (Node) | DynamoDB | yes | yes, through the running app |

### Which client library, and where it is exercised

A capability is not enough to say what a program gets back. Two Redis libraries
have different APIs and a synchronous SQLAlchemy engine is not an asynchronous
one, so the compiler records the *library* the source constructed and the shim
returns one of the same kind. Each of those paths is exercised by a named
example, deployed and driven, rather than by a unit test alone:

| Client library | Example | What it becomes |
|---|---|---|
| `sqlalchemy` | petstore-multi (worker), mixed (worker) | RDS Postgres |
| `sqlalchemy-async` | petstore-multi (api) | RDS Postgres |
| `redis-py` | kitchen-sink | ElastiCache |
| `redis-py-async` | petstore-multi (api) | ElastiCache |
| `pathlib` | petstore-multi (worker), mixed (worker), nomnom | S3 |
| `boto3-dynamodb` | petstore, petstore-multi, mixed, nomnom | DynamoDB |
| `pg` | mixed (api) | RDS Postgres |
| `ioredis` | mixed (api) | ElastiCache |
| `aws-sdk-s3` | mixed (api) | S3 |
| `aws-sdk-dynamodb` | mixed (api), petstore-node | DynamoDB |
| `knex`, `node-redis` | tests/e2e/node-clients.sh only | RDS Postgres, ElastiCache |

`petstore-multi` deliberately holds both halves of the SQLAlchemy pair: the api
unit's engine is asynchronous and the worker's is synchronous, against two
databases in one application. `examples/mixed` holds the cross-language version
of the same claim -- one Postgres instance reached through `pg` from Node and
through an ORM from Python, and one bucket reached through an `S3Client` and a
`pathlib.Path`.

The last row is the honest one: `knex` and `node-redis` are covered against real
servers by `tests/e2e/node-clients.sh`, but no example declares them, so nothing
proves they survive a full compile and deploy.

"Preview only" means the resource is checked through `pulumi preview` rather
than actually created. Nothing is preview only any more: LocalStack runs Fargate
tasks as real containers, and `examples.sh` waits for one and asks the service
how many are running.

**RDS and ElastiCache are no longer preview only.** The emulator provisions
both, and LocalStack does start a real PostgreSQL behind an RDS instance -- an
earlier emulator did not, which is why the harness runs engines of its own and
redirects the bindings at them. That redirect is now belt and braces for RDS
rather than the only thing making it work, and still load-bearing for the cache.
A scenario declares which engines it needs:

```json
"engines": ["postgres", "redis"]
```

That is the same bargain as the emulator itself: a stand-in for the managed
service rather than a mock of it. A program that talks SQL talks it to an actual
Postgres, and a cache miss is an actual cache miss.

Both engine bindings are redirected, and it is worth being exact about what that
costs. The emulator reports where its instance and cluster are *on its own
network* -- a container IP on CI, localhost on a desktop Docker that publishes
ports -- and neither is where the engine is, because the emulator runs none. So
the harness replaces the host and port, and only those: the URL's scheme, user
and database name are the compiler's and are used as emitted. The shape of the
binding is tested; the address in it is not, which is the most that can be
checked against a control plane with nothing behind it.

This was written as "only the cache needs redirecting" first, which was true on
the machine it was written on. CI reports the database at a container IP, and
the compiled program reached the emulator's own address and was asked to
authenticate: `fe_sendauth: no password supplied`.

`examples/petstore-multi` is the example that exercises this: two units, a
DynamoDB table, an S3 bucket, an SNS topic, a relational catalogue, a cache, a
managed secret, a static site and a config value -- ten capability kinds, all
deployed and all driven.

Keep this table current. It is the honest answer to "is that capability really
tested?", and a stale one is worse than none.

## What each test is defending

Some tests exist for a specific failure that would otherwise be easy to ship:

| Test | What it prevents |
|---|---|
| `TestCompileIsDeterministic` | Non-determinism creeping into generation; both golden testing and the deploy fingerprint rest on it |
| `TestMultiUnitSharesOneStore` | Two units silently getting two tables — the case Klotho 1 shipped the machinery for but never exercised |
| `TestIAMIsLeastPrivilege` | A unit reaching a store it never declared, or a wildcard resource grant |
| `TestStaticAssetsNeverEnterAComputeBundle` | The plugin ordering regressing, which would bloat every bundle with site assets |
| `TestSecretsNeverAppearInGeneratedSource` | A secret config value being inlined as plaintext |
| `test_shim_parity.py` | The SDK stub and the injected client drifting, so a program that works locally fails once compiled |
| `parity_test.go` | The compiler and the shims spelling an environment variable differently |
| `TestTheCompilePathCannotReachTheNetwork` | A compile-time dependency on something remote |
| `TestPreflightRefusesStaleOutput` | Deploying output that no longer matches the source |
| `TestRuntimeFilesAreEmbedded` / `assertParses` | Shipping shims that are not valid Python |
| `TestCorpus` | Any of the five bugs the generator found coming back |
| `TestPhysicalNamesAreUnique` | Two capability ids silently sharing one cloud resource |
| `TestGenerationIsReproducible` | A failing seed that cannot be reproduced |
| `differential.sh` | Compilation changing what a program does |
| `load.sh` | An edge that is wired and dead: a store nothing writes to, a subscriber that never wakes, a unit nobody invokes |

## Golden trees

Golden output lives in `internal/cli/testdata/golden/<example>/`. To accept a
change:

```bash
go test ./internal/cli -update
git diff internal/cli/testdata          # read this before committing it
```

The diff is the point. A change you cannot explain is a bug you have not
noticed yet.

## CI

Two jobs, in `.github/workflows/ci.yml`:

- **offline** — `gofmt`, `go vet`, the Go suite and the Python suite. The Go
  suite then runs a second time inside a container with `--network none`, which
  is what actually proves compilation needs no network: not a disabled proxy, no
  network namespace at all. The module cache is mounted in, so a stray fetch
  fails loudly instead of silently succeeding.

  You can run that second pass locally:

  ```bash
  docker run --rm --network none \
    -v "$PWD":/src -w /src \
    -v "$(go env GOMODCACHE)":/go/pkg/mod \
    -e GOPROXY=off -e GOFLAGS=-mod=mod \
    golang:1.26-bookworm go test ./... -count=1
  ```
- **integration** — L3 to L5 with the emulator as a service container.

A nightly job can run the same scripts against real AWS by leaving
`CLOUDCC_EMULATOR_ENDPOINT` unset; it is off by default because it costs money.


## The emulator

LocalStack Pro, started by `tests/e2e/localstack.sh`. Every harness reads its
endpoint from `$CLOUDCC_EMULATOR_ENDPOINT` and nothing else, which is what made
moving off the previous emulator a matter of settings rather than code.

Four settings are not optional, and each was found by watching something fail:
the auth token (EKS, ECS, RDS and ElastiCache are Pro services),
the Docker socket (Lambda runs in containers and EKS is backed by a real
Kubernetes), `LAMBDA_IGNORE_ARCHITECTURE` (a function declares x86_64 and an
Apple Silicon host is not, and without it every invoke fails with
`Could not start new environment: ContainerException`, which names neither),
and `LOCALSTACK_HOST=localhost:4566`, which makes ECR hand out repository URLs
under `*.localhost` rather than under a public domain that does not resolve
where DNS is restricted.

Pushing to that ECR needs Docker to treat it as insecure -- Docker requires
HTTPS for any host that is not literally `localhost`, and the certificate covers
a different name. On colima:

```yaml
# ~/.colima/default/colima.yaml
docker:
  insecure-registries:
    - "000000000000.dkr.ecr.us-east-1.localhost:4566"
```

Wildcards are rejected by dockerd, which wants the exact host, and the symptom
is a daemon that will not start at all.

## Kubernetes on the emulator

`tests/e2e/kubernetes.sh` deploys a container unit with `platform: kubernetes`.
This is the least mocked thing in the suite: the emulator backs an EKS cluster
with a real `rancher/k3s` container, so `pulumi up` talks to an actual API
server, and the pod it describes is scheduled, pulled and started.

**It needs the Docker socket.** Without it the cluster is a stub: ACTIVE, with
an endpoint nothing listens on. The harness detects that and says so rather than
timing out.

*Credentials are the real ones.* `aws eks get-token` is accepted, so the
kubeconfig the compiler generates is used as written -- endpoint, certificate
authority and exec plugin, exactly as against AWS. Only TLS verification is
skipped, because the certificate is issued for a hostname this machine cannot
resolve, and the address is not what is under test. `CLOUDCC_KUBECONFIG` exists
as a fallback for an emulator that rejects the token, and is unused here.

*The registry is the real one too.* The image is pushed into the emulator's own
ECR, under the repository the compiler provisioned, and the pod pulls it from
there -- the same round trip a real deployment makes. Side-loading the image
into the node would be easier and would prove nothing about the pull. Two things
have to be arranged for that, and neither is about the compiler: the host must
be allowed to push over plain HTTP (above), and inside the cluster the
registry's hostname has to point at the emulator rather than at the node, which
the harness writes into the node's `/etc/hosts` and containerd's `certs.d`.

*The cluster has no workers.* Its one node is the control plane, which
Kubernetes taints so workloads stay off it; real EKS has worker nodes from a
node group, and the emulator records the node group as an API object without
starting any machines. The harness removes the taint, which is what a one-node
development cluster means. Nothing in the Deployment changes.

What remains unverified, and is reported as such rather than failed: a
`LoadBalancer` Service is never given an external address, because there is no
cloud load balancer. The Service exists and routes; the test reaches it by
port-forward, which is what the address would have been for.

What is not covered at all: a pod's AWS identity. IRSA is not emitted, so a
Kubernetes unit that reaches an AWS store is warned about at compile time. On
the emulator it would work anyway -- nothing there authenticates -- which is
exactly why the warning is at compile time and not left to a test to discover.
