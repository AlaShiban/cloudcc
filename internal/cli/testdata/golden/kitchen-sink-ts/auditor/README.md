# kitchen-sink, in TypeScript

Every capability the compiler supports, in one program, and the only TypeScript
example with **both compute types**: `src/app.ts` is a function behind an HTTP
API, `src/reporter.ts` is a container behind a load balancer. They share
`src/stores.ts`, so both are wired to the same table, bucket and topic.

| what the program holds | becomes |
|---|---|
| a `DynamoDBClient` | DynamoDB |
| an `S3Client` | S3 |
| a `pg` `Pool` | RDS Postgres |
| an `ioredis` client | ElastiCache |
| a `Secret` | Secrets Manager |
| a `Topic<ItemEvent>` | SNS |
| `configValue(...)` | an environment variable, and a stack secret when `secret: true` |
| `embedAssets("../data/*.json")` | files bundled with the declaring unit |

## What this one adds

**A typed channel.** `new Topic<ItemEvent>()` means the publisher and every
subscriber are checked against one another, so renaming a field on one side is a
compile error rather than an `undefined` in a store nobody reads. A bare
`new Topic()` still carries `Json` and still works — the type parameter is opt-in,
and it is the reason the SDK's `Topic` is generic at all.

**One language, two packagings.** The api becomes a zip Lambda loads; the
reporter becomes an image that runs the program itself. Both come out of the
same bundler with the types stripped the same way, and the only thing that says
which is `type:` in `cloudcc.yaml`.

**A portable setting that is not portably valued.** `memory:` means the same
thing on both — megabytes the application gets — but a function takes anything
from 128 MB in 1 MB steps while Fargate takes 512 and then multiples of 1024. So
`memory: 2048` is fine on the reporter and `memory: 1500` would be a compile
error there and legal on the api. Each is checked against the host it lands on.

```bash
docker run -d -p 5432:5432 -e POSTGRES_USER=ccadmin -e POSTGRES_DB=shopdb \
  -e POSTGRES_HOST_AUTH_METHOD=trust postgres:16-alpine
docker run -d -p 6379:6379 redis:7-alpine
npx tsx src/app.ts
```
