# What Klotho has that `cloudcc` doesn't

Read from `/Users/alashiban/Projects/klotho` on 2026-08-29 (`main` at `c50c3a05`).
Two items have since been implemented in `cloudcc` and are struck through below.

Two different products live in that repository and the distinction matters for
this list:

- **Klotho 1** — the annotation-driven Infrastructure-from-Code compiler. This is
  the thing `cloudcc` is a redesign of. Its source is **not on `main`**; it was
  removed by the Klotho 2 rewrite and survives at tag `pre-ifc2` (`d451c157`,
  2023-04-26). Everything below about Klotho 1 is taken from `spec.md`, the
  architecture document written from that tree — **I did not read the v1 source
  itself**, so treat those entries as "documented as supported", not verified.
- **Klotho 2** — what `main` actually contains (`pkg/k2`, `pkg/engine`,
  `pkg/knowledgebase`, `pkg/templates`). A different product: a Python IaC SDK plus
  a constraint-solving engine and a Pulumi-automation orchestrator. It does no
  source analysis at all. Its entries below are verified against files in the tree.

Where an item is not a real gap I've said so rather than padding the list. A
closing section records the places `cloudcc` is ahead, so the list isn't read as
a scorecard in one direction.

---

## 1. Klotho 1 — the directly comparable product

### 1.1 Capability declaration

| | Klotho 1 | `cloudcc` |
|---|---|---|
| How a capability is marked | `@klotho::persist { id = "x" }` in a **comment**, TOML directives, bound to the next AST node | a call to the `cloudcompiler` SDK |

The consequence is the feature: annotated source has **no import of anything**, so
it compiles under any toolchain that ignores comments, and the marks survive being
copied into a codebase that has never heard of the compiler. `cloudcc` requires
`import cloudcompiler as cloudcc` and a real call. (`cloudcc` gets type-preservation
and literal-argument checking in exchange — a genuine trade, not a straight loss.)

### 1.2 Source languages

| Language | Klotho 1 | `cloudcc` |
|---|---|---|
| Python | full | full |
| JavaScript / TypeScript | full | present (`internal/lang/node`) |
| **Go** | early-access frontend (`pkg/lang/golang`) | **none** |
| **C#** | early-access frontend (`pkg/lang/csharp`) | **none** |

### 1.3 Web frameworks

Klotho 1 detects routes in **Express** and **NestJS**. `cloudcc` detects FastAPI
(Python) and Express/Fastify (Node) — **NestJS is the gap**.

### 1.4 Things Klotho 1 emits that `cloudcc` has no path to

- ~~**CloudFront CDN.**~~ — **closed, mostly.**
  `static_units.<id>.type: cloudfront` now emits a distribution, an origin access
  identity and a bucket policy, and binds the site's URL to the distribution's
  domain. What is left of the gap is the *sharing*: Klotho 1's
  `content_delivery_network.id` groups a static site and an API Gateway behind
  **one** distribution, and `cloudcc` gives each static unit its own.
- **API Gateway REST (v1)** as the expose target. `cloudcc` emits HTTP API (v2) or
  an ALB. Not obviously worse, but REST-only features (request validation, usage
  plans, per-method integrations) are unreachable.
- **User-supplied Helm charts on EKS.** `helm_chart_options: { directory, install }`
  loads *your* chart, renders it with the Helm Go SDK, and injects Klotho-managed
  values (image refs, env, bindings). `cloudcc` emits a generated
  Deployment + Service and has no Helm path.
- **`network_placement: private`** per execution unit. `cloudcc`'s VPC is a single
  public tier — `internal/provider/aws/network.go` creates a VPC, an internet
  gateway, subnets, a route table and a security group, and **no NAT gateway**, so
  there is nothing to place a unit privately *into*.
- **CockroachDB Serverless** as an alternate ORM backend, implemented by
  **rewriting the user's `new Sequelize(...)` call** to the Cockroach driver and
  injecting the `require`. `cloudcc` accepts `cockroachdb` in the schema and
  refuses it at compile time.

### 1.5 Ingestion and packaging

- **Monorepo manifest discovery** — walks *upward* from the configured root to find
  `package.json` / `requirements.txt` / `go.mod` / `*.csproj`.
- **User-authored Dockerfile respected** — `runtime.ShouldOverrideDockerfile` uses
  your annotated `Dockerfile` when present instead of the generated one.

### 1.6 Not gaps, despite appearing in Klotho 1

`persist` KV/FS/ORM/secret/redis-node/redis-cluster, MemoryDB for clustered Redis,
SNS pub/sub, `embed_assets`, `config`, layered config defaults, `pulumi_params`
passthrough, resolved-config round-trip, Pulumi TypeScript output, runtime shim
injection — `cloudcc` has all of these. Login, telemetry and self-update are in
Klotho 1 and deliberately excluded from `cloudcc`. The topology diagram is a
*regression* in Klotho 1, not a feature: it POSTs a generated `diagrams` script to
a hardcoded Klotho-hosted HTTP endpoint with no offline fallback.

---

## 2. Klotho 2 — features from a different architecture

### 2.1 A constraint-solving engine (`pkg/engine`)

`cloudcc` resolves capabilities to resources with a direct mapping in
`internal/provider/aws/resolve.go`. Klotho 2 *solves* for the graph:

- **Five constraint scopes** — `application`, `construct`, `resource`, `edge`,
  `output` — with operators `must_exist`, `must_not_exist`, `add`, `remove`,
  `replace`, `import`, `equals`. A user states intent ("these two must not be
  connected", "this resource must be that type") and the engine finds a graph
  satisfying it.
- **Path selection / expansion** (`pkg/engine/path_selection`) — given "A must
  reach B", it enumerates candidate paths, scores them by weight and validity, and
  materialises the intermediate resources. Nothing in `cloudcc` invents a resource
  that wasn't named by a capability.
- **Operational rules and evaluation** (`operational_rule`, `operational_eval`) —
  a dependency-ordered evaluator over property/edge/resource vertices, so resource
  configuration is *derived* rather than templated.
- **Graph reconciler** and **`--dry-run` at three levels** (Pulumi preview, `tsc`
  only, files only). `cloudcc` has `--preview` only.

### 2.2 A knowledge base instead of a code path per resource (`pkg/templates`)

| | Klotho 2 | `cloudcc` |
|---|---|---|
| AWS resource templates | **288** YAML | 45 Pulumi types in `registry.go` |
| Kubernetes resource templates | **58** YAML | 3 (`Provider`, `Deployment`, `Service`) |
| **Edge templates** | **204** YAML | none — edges are Go code |

The edge templates are the piece with no `cloudcc` equivalent: each file
(`lambda_function-dynamodb_table.yaml`, `ec2_instance-efs_mount_target.yaml`, …)
declares *how any two resource types connect* — IAM, security-group rules,
environment variables. Adding a resource pair is a YAML file, not a code change.

### 2.3 AWS resources Klotho 2 models and `cloudcc` cannot emit

Networking: **NAT gateway, elastic IP, VPC endpoint, private DNS namespace,
service-discovery HTTP namespace and service**.
Compute: **EC2 instance, AMI, launch template, auto-scaling group, App Runner
service, ECS capacity providers, EKS Fargate profiles, EKS add-ons, IAM OIDC
provider (IRSA)**.
Data: ~~SQS, Lambda event-source mapping~~ (**closed** — a topic with one
subscriber now provisions a queue and a mapping that consumes it), **EFS (file
system, mount target, access point), RDS Proxy (+ target group), ElastiCache
parameter group, MemoryDB ACL and user**.
Edge/API: ~~CloudFront distribution + origin-access identity~~ (**closed** — see
§1.4), **ACM certificate, listener certificate, load-balancer listener rules,
API Gateway REST (rest_api, resource, method, integration, deployment, stage,
VPC link)**.
Ops: **CloudWatch alarms and dashboards, SES email identity, S3 bucket
objects, IAM policy / role-policy attachment / instance profile**. (S3 bucket
policy is now emitted, for the CDN origin.)

Kubernetes: **HorizontalPodAutoscaler, PersistentVolume + PVC + StorageClass,
ConfigMap, Namespace, ServiceAccount, Helm chart, Kustomize directory, raw
manifest, Pod, TargetGroupBinding, ClusterSet + ServiceExport** (multi-cluster).

### 2.4 Lifecycle and state

- **A URN scheme** (`pkg/k2/model/urn.go`): account / project / environment /
  application / resource / output. `cloudcc` identifies things by app name and
  Pulumi stack.
- **First-class environments** — `State{ProjectURN, AppURN, Environment,
  DefaultRegion, Constructs}` with per-construct status and last-updated time,
  persisted and versioned (`schemaVersion`). `cloudcc` has `.cloudcc-state.json`,
  which is a staleness fingerprint, not a lifecycle record.
- **Per-construct Pulumi stacks with an orchestrated up/down DAG**
  (`pkg/k2/orchestration`) — deploy order derived from construct dependencies,
  each construct its own stack. `cloudcc` deploys one stack per app.
- **Importing existing infrastructure** — `import_resources.go` plus the `import`
  constraint operator, and `pkg/infra/state_reader` reads an existing Pulumi state
  back into the resource graph. `cloudcc` has nothing that adopts a pre-existing
  resource (grep for import/adopt in `internal/` returns nothing).
- **Signal-safe cleanup** (`pkg/k2/cleanup`) — handlers run on SIGTERM and on
  panic, so an interrupted deploy tears down its scratch state.
- **A TUI** (`pkg/tui`) with per-construct progress, parsed npm and Pulumi progress
  streams, and verbosity levels. `cloudcc` prints lines.

### 2.5 Authoring model

- **A Python IaC SDK** (`klothosdk`): `klotho.Application(...)`, then
  `aws.Container / Function / Api / Bucket / DynamoDB / Postgres / Network /
  FastAPI`. The program *is* the infrastructure declaration.
- **A gRPC language host** (`service.proto`, `python_language_host.py`) — the Go
  binary runs the user's Python infra program in a subprocess and receives the IR
  over gRPC. This is the mechanism `cloudcc` deliberately does not have: `cloudcc`
  reads source statically and never executes it.
- **Construct bindings as templates** — `to_klotho.aws.Postgres.yaml`,
  `from_klotho.aws.Api.yaml`: how one construct wires to another is data.
- **`klotho init`** scaffolding, and **structured inputs + for-each rules** in
  construct templates (`58d73f6c`).

---

## 3. Where `cloudcc` is ahead

So the list above isn't mistaken for a full comparison:

- **Multiple execution units actually work.** Klotho 1's `spec.md` records that
  only one unit was ever created despite the splitting machinery existing;
  `cloudcc` splits along the import graph and has `examples/petstore-multi`.
- **The client decides the resource.** `persist(Redis(...))` vs
  `persist(create_async_engine(...))` resolves capability *and library* from the
  code. Klotho 1 read the capability from an annotation and took the backend type
  from static config defaults — it could not tell a sync SQLAlchemy engine from an
  async one.
- **Type-preserving `persist`** — the program runs uncompiled against local
  Redis/Postgres/directories, and the compiled form returns a client of the same
  type. Klotho 1 injected its own client wrappers.
- **Local, offline diagram rendering** in three notations and two layers, versus
  Klotho 1's hardcoded remote PNG service.
- **Pub/sub selected from declared requirements** (ordering, replay, fan-out,
  retention, payload size → SNS / SNS FIFO / SQS / SQS FIFO / Kinesis), and now
  **two of those are provisioned** rather than one: `subscribers="many"` is SNS,
  `subscribers="one"` is a queue with an event source mapping, and the visibility
  timeout is derived from the subscriber's own timeout. Klotho 1 mapped `PubSub`
  to SNS, full stop.
- **Klotho 2 does no source analysis at all** — every §2 feature is bought by
  giving up Infrastructure-from-Code entirely.

---

## 4. If any of this is to be picked up

Ranked by what it would buy against what it would cost, in my read:

1. **NAT gateway + private subnets** — small, self-contained, and it unblocks
   `network_placement: private`, RDS in a private tier, and VPC endpoints.
2. ~~SQS + Lambda event-source mapping~~ — **done.**
3. ~~CloudFront in front of a static unit~~ — **done.**
4. **Adopting existing resources** — the thing that decides whether this can be
   introduced into a live account rather than only a new one.
5. **Edge templates as data** — the largest change and the one that would most
   reduce the per-resource-pair cost, but it is an architectural rewrite of
   `internal/provider/aws`.
