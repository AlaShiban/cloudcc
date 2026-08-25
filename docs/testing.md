# Testing

## The pyramid

| Layer | What it checks | Network | Command |
|---|---|---|---|
| **L1 unit** | Each package in isolation: config merging, the scheduler, tree-sitter detection, name sanitising, the rewriter, diagnostics | none | `go test ./...` |
| **L2 golden** | Whole compiled trees for three examples, plus a double-run diff proving byte determinism | none | `go test ./internal/cli` |
| **L3 type/shape** | `tsc --noEmit` on the generated project under `strict` | none | inside the e2e harness |
| **L4 provisioning** | `pulumi up` against the emulator, then assert through the AWS CLI | emulator | `./tests/e2e/ministack.sh` |
| **L5 functional** | Run the compiled application against those resources; assert HTTP responses *and* datastore state | emulator | `./tests/e2e/ministack.sh` |
| **Python** | SDK emulations, and their signature parity with the injected shims | none | `cd sdk/python && uv run --with pytest --with-editable . python -m pytest tests` |
| **Generated corpus** | Twenty generated programs compiled and checked against the generator's own ground truth | none | `go test ./internal/fuzz` |
| **Differential** | The same generated program run uncompiled and compiled; every response must match | emulator | `./tests/e2e/differential.sh` |

```bash
go test ./...                              # L1 + L2
./tests/e2e/ministack.sh                   # L3 + L4 + L5, through raw pulumi
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
| `execution_unit` (ecs) | ECS Fargate | preview only | Dockerfile shape only |
| `persist(create_engine(...))` | RDS Postgres | preview only | optional, against a local Postgres |
| `persist(Redis())` | ElastiCache | preview only | optional, against a local Redis |

"Preview only" means the resource is checked through `pulumi preview` rather
than actually created: creating an RDS instance or a Fargate service in an
emulator is slow and its behaviour is not faithful enough for the result to
mean much.

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
`MINISTACK_ENDPOINT` unset; it is off by default because it costs money.
