# petstore-multi, in TypeScript

The breadth example: two units, one shared module, and every stateful
capability the compiler supports — in the language where the types have to
survive the rewrite.

| id | `api` holds | `worker` holds | becomes |
|---|---|---|---|
| `petsByOwner` | a `DynamoDBClient` | the same client, same id | DynamoDB |
| `petsdb` | a **Knex** instance | — | RDS Postgres |
| `petCache` | a **node-redis** client | — | ElastiCache |
| `auditdb` | — | a `pg` `Pool` | RDS Postgres (a second one) |
| `petAudit` | — | an `S3Client` | S3 |
| `auditKey` | — | a `Secret` | Secrets Manager |
| `petEvents` | publishes | subscribes | SQS (one subscriber) |
| `petstore-ts-site` | `public/**` | — | S3 behind CloudFront |

## What it covers that nothing else does

**Knex and node-redis, deployed.** Until this example both were exercised by
`tests/e2e/node-clients.sh` alone, which proves a shim can connect — not that a
program using one survives a compile and a deploy. Two libraries for one
capability appear here in one application: the api's Knex and the worker's `pg`
Pool are both `persist_orm`, against two different databases.

**Two ways a compiled store keeps its type.** `new DynamoDBClient()` is a
constructor, so the compiler names the class and the rewritten line reads
`_cloudccKv.connect("petsByOwner") as DynamoDBClient`. `knex(...)` and
`createClient(...)` are *factories* — there is no class to name — so those two
carry an annotation instead:

```ts
const db: Knex = persist(knex({ client: "pg", connection: "…" }), { id: "petsdb" });
```

Rewriting replaces the `persist(...)` call and nothing else, so `const db: Knex =`
survives into the compiled copy and the type comes back. Annotating a
factory-built store is the TypeScript habit worth having.

**The `externals` escape hatch, for real.** Knex statically requires every
dialect driver it supports, so the bundler sees `require("mysql")`,
`require("oracledb")` and five more that are not installed. They are never
*called* — this connection is `client: "pg"` — so `pulumi_params.externals` in
`cloudcc.yaml` tells the bundler to leave them alone. Nothing else in the
repository uses that hatch.

## Least privilege out of the import graph

The worker never imports `shared/catalogue.ts`, so it has no access to `petsdb`
and no cache binding; the api never imports `shared/ledger.ts` or
`shared/signing.ts`, so it has neither the audit database nor the secret. Both
import `shared/store.ts`, which is why they share one table and one queue.

Nobody maintains that as a list. It falls out of what each unit bundles.

## Running it

```bash
docker run -d -p 5432:5432 -e POSTGRES_USER=ccadmin -e POSTGRES_DB=petsdb \
  -e POSTGRES_HOST_AUTH_METHOD=trust postgres:16-alpine
docker run -d -p 6379:6379 redis:7-alpine
npx tsx src/api.ts
```
