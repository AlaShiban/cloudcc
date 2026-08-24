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

```bash
go test ./...                              # L1 + L2
./tests/e2e/ministack.sh                   # L3 + L4 + L5, through raw pulumi
./tests/e2e/deploy.sh                      # the same path through `cc deploy`
cd sdk/python && uv run --with pytest --with-editable . python -m pytest tests
```

`CC_E2E_KEEP=1` leaves the work directory and the deployed stack in place for
inspection; the harness prints the command to tear it down.

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
| `persist_kv` | DynamoDB | yes | yes, through the running app |
| `persist_fs` | S3 | yes | yes, through the running app |
| `persist_secret` | Secrets Manager | yes | yes, through the running app |
| `pubsub` | SNS + subscription | yes | publish reaches the subscriber |
| `static_unit` | S3 website | yes | object fetch |
| `expose` | API Gateway v2 | probe-dependent | via local uvicorn/Mangum instead |
| `execution_unit` (lambda) | Lambda | probe-dependent | direct handler invoke |
| `execution_unit` (ecs) | ECS Fargate | preview only | Dockerfile shape only |
| `persist_orm` | RDS Postgres | preview only | optional, against a local Postgres |
| `persist_redis` | ElastiCache | preview only | optional, against a local Redis |

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

- **offline** — L1 and L2 with networking disabled, which is what actually
  proves compilation needs no network. Also runs `gofmt`, `go vet` and the
  Python suite.
- **integration** — L3 to L5 with the emulator as a service container.

A nightly job can run the same scripts against real AWS by leaving
`MINISTACK_ENDPOINT` unset; it is off by default because it costs money.
