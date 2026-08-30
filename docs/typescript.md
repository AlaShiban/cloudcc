# Every capability, in TypeScript

The companion to the gallery in [the README](../README.md#every-capability-by-example),
which is written in Python. Same capabilities, same order, same resources —
only the spelling differs.

Two differences are real rather than cosmetic, and both come from the
libraries rather than from `cloudcc`:

* **Options go in an object.** `persist(client, { id })`, not `persist(client, id=...)`.
  The rules are unchanged: the id is required, explicit, and must be a literal.
* **A file store is an `S3Client`, not a path.** Node has no `pathlib`, so
  there is no local type with the same shape to preserve. Python persists a
  `Path` and gets a `cloudpathlib.S3Path`; TypeScript persists the AWS client
  and keeps it.

Everything else — one verb for state, ids never inferred from variable names,
the client deciding the resource — reads the same in both.

```ts
import {
  persist, expose, executionUnit, remote, configValue,
  staticUnit, embedAssets, Topic, Secret,
} from "@cloudcompiler/sdk";
```

Run one uncompiled with anything that runs TypeScript:

```bash
npx tsx server.ts        # or: node --import tsx server.ts
```

---

## An execution unit

```ts
executionUnit({ id: "api" });
```

→ a Lambda. `type: container` in `cloudcc.yaml` makes it ECS Fargate;
`platform: kubernetes` makes it a Deployment on EKS. The code does not change.

## Exposing HTTP

```ts
const app = express();
expose(app, { id: "pet-api" });
```

→ API Gateway v2 in front of the unit, or an ALB for a container unit.
`cloudcc` reads *which variable* holds the app; your framework still owns
routing.

## Key/value — DynamoDB

```ts
export const catalogue = persist(new DynamoDBClient({}), { id: "catalogue" });

await catalogue.send(new PutItemCommand({
  TableName: "catalogue",                 // rewritten to the provisioned table
  Item: { id: { S: "1" }, name: { S: "rex" } },
}));
```

→ a DynamoDB table. You keep a `DynamoDBClient`, so every command the AWS SDK
has works. Whatever table name you wrote locally is rewritten to the
provisioned one, so the two halves need not agree on a string.

## Relational — RDS

```ts
export const db = persist(
  new Pool({ connectionString: "postgresql://ccadmin@localhost:5432/shopdb" }),
  { id: "shopdb", models: ["Item", "Order"] },
);

const { rows } = await db.query("SELECT id, name FROM items ORDER BY id");
```

→ RDS Postgres. `knex({ client: "pg", connection: "…" })` works the same way
and compiles to the same thing. The master credential is managed by AWS and
never enters an environment variable.

## Cache — ElastiCache

```ts
export const cache = persist(new Redis({ host: "localhost", port: 6379 }), {
  id: "itemCache",
});

await cache.setex(`item:${id}`, 300, summary);
```

→ ElastiCache. `ioredis` and node-redis's `createClient({ url })` are both
recognised. `type: memorydb` in `cloudcc.yaml` switches the service without
touching this line.

## Files — S3

```ts
export const DOCS = "item-docs" as const;   // a bucket name, not the id
export const docs = persist(new S3Client({}), { id: "itemDocs" });

await docs.send(new PutObjectCommand({
  Bucket: DOCS,                             // rewritten to the provisioned bucket
  Key: `${itemId}.json`,
  Body: JSON.stringify(body),
}));
```

→ an S3 bucket, with the bucket name rewritten the same way table names are.

**The name and the id are two different things here**, and the difference bites
in TypeScript in a way it does not in Python. The Python mirror persists
`Path("./itemDocs")` — a directory, which may be spelled however you like.
This store is an `S3Client` and the name travels in every command, so it has to
be a *legal bucket name*: lowercase, no underscores. `itemDocs` compiles and
then fails at the API. Uncompiled, that name is the bucket your program really
talks to; compiled, the shim rewrites it.

## Pub/sub — SNS, or SQS

```ts
export interface ItemEvent { action: string; id: string; }

export const events = persist(new Topic<ItemEvent>(), { id: "itemEvents" });

await events.publish({ action: "upserted", id });   // publisher
events.subscribe(onItemEvent);                      // subscriber, another unit
```

→ SNS for fan-out. `new Topic({ subscribers: "one" })` is a work queue and
compiles to SQS with an event-source mapping instead. The type parameter is the
channel's: publisher and every subscriber are checked against one another.

## Secrets

```ts
export const signingKey = persist(new Secret(), { id: "signingKey" });

const key = await signingKey.get();
```

→ Secrets Manager. `cloudcc` provisions the secret and never its value, so
nothing sensitive is in the generated project or the state file. Uncompiled it
reads `CLOUDCC_SECRET_SIGNINGKEY`, so a local run and a deployment can be given
the same value.

## Calling another unit

```ts
import * as summaryModule from "./summary.js";

const summary = remote(summaryModule, { id: "summary" });

const text = await summary.summarize(pet);
```

→ a Lambda invoke. Uncompiled it is an ordinary in-process call. The functions
must be `async`, the names must exist, and the calls may not form a cycle —
all three are compile errors rather than runtime surprises.

## Configuration values

```ts
const logLevel = configValue("log_level", { default: "info" });
const stripeKey = configValue("stripe_key", { secret: true });
```

→ an environment variable on the unit; `secret: true` makes it a Pulumi stack
secret, encrypted in state.

## Assets bundled with a unit

```ts
const SEED = embedAssets("../data/*.json");
```

→ the matching files travel inside the unit's own bundle, and the call returns
the directory they landed in.

## A static site, optionally behind a CDN

```ts
staticUnit("petstore-ts-site", {
  staticFiles: "../public/**/*",
  indexDocument: "index.html",
});
```

→ an S3 website. `type: cloudfront` in `cloudcc.yaml` keeps the bucket private
and puts a distribution in front of it instead.

## Logging

```ts
console.log("started");
```

→ CloudWatch, configured before your code is imported. Where logs go is
`cloudcc.yaml`'s business, not your program's.

## Seeing what it did

```bash
CLOUDCC_TRACE=1 npx tsx server.ts 2> trace.log
cloudcc trace trace.log
```

→ every call through a persisted client, grouped by the id you gave it. See
[the README](../README.md#seeing-what-it-actually-did).

---

## Working TypeScript examples

Each is a mirror of the Python example beside it, and both are deployed and
compared by the same suite.

| Example | Covers |
|---|---|
| `examples/petstore-ts` | one unit, `type: container` behind an ALB, tsconfig `paths` alias |
| `examples/petstore-node` | plain JavaScript, two units |
| `examples/petstore-multi-ts` | knex + node-redis, a topic on SQS, a CloudFront site |
| `examples/kitchen-sink-ts` | every store type at once, Fargate, a subscriber |
| `examples/nomnom-ts` | six units across Lambda, Fargate and Kubernetes |
| `examples/mixed` | a TypeScript unit and a Python unit sharing one table |
