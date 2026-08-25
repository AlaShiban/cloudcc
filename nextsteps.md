# Next steps

Where the work stands and what is left, written so a future session can pick it
up without re-deriving any of it.

State at time of writing: `main` is ahead of `origin/main` and **nothing has
been pushed**. The Go suite, the Python SDK tests (29) and the Node SDK tests
(33) all pass locally; the differential harness reports identical behaviour on
seeds 1–3; and the Node client shims connect to real servers.

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

## 2. N4 — deploy a Node app to the emulator

From `docs/plan-node.md`. N0–N3 are met; this is the first unmet gate.

**Scope:** `expose`/routes for Node, resolve to Lambda + API Gateway v2, deploy.

**Gate:** ministack L4/L5 for a Node app — up, assert, drive, destroy. The
existing `tests/e2e/ministack.sh` is the model; it is Python-only today.

This gate matters more than its position in the list suggests, because of §4
below: it is what would turn a pile of reasoning into evidence.

---

## 3. N5 — Node differential harness, and cross-language IR equivalence

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

## 5. The generator only emits Python

Left over from N2. `internal/fuzz` generates Python programs; there is no Node
renderer. Consequences:

- `TestCorpus` and `TestSweep` exercise only the Python frontend
- `TestCorpusCoversEveryClientShape` pins Python client spellings only; the Node
  table (`nodeClients` in `internal/sdkdetect/client.go`) has no equivalent
- §3 is blocked on this

The generator is already structured for it: `internal/fuzz/generate.go` builds a
language-neutral shape and `internal/fuzz/python.go` renders it. A `node.go`
alongside would slot in. The renderer is the work; the shapes are done.

Worth carrying over: the corpus is seed-pinned
(`CorpusSeeds` in `internal/fuzz/sweep_test.go`) and the shape-coverage test is
what keeps that hand-chosen list honest. It has already failed twice on real
gaps — once when `persist()` started reading client types, once when async
SQLAlchemy engines were added. Build the Node equivalent the same way.

---

## 6. N6 — containers, mixed-language applications

**Scope:** ECS/container units for Node, mixed-language applications, docs.

**Gate:** a mixed app compiles and deploys.

The architecture already supports this — a frontend is chosen *per execution
unit* from its entrypoint's extension, so a Python worker beside a Node API is
not a special case. It has simply never been tested. `docs/plan-node.md:53`
notes this explicitly.

---

## Suggested order

1. ~~CI Node job~~ — done
2. ~~Prove the shims run~~ — done, and it found a bug
3. **N4** — deploy a Node app; also covers the managed-secret branch §4 leaves open
4. **N5**, which needs **§5** first
5. **N6**

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
