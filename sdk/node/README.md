# @cloudcompiler/sdk

The hint SDK for [CloudCompiler](../../README.md), for TypeScript and
JavaScript. The Python SDK is [`cloudcompiler`](../python/README.md).

```ts
import express from "express";
import { DynamoDBClient } from "@aws-sdk/client-dynamodb";
import { persist, expose } from "@cloudcompiler/sdk";

const app = express();
const pets = persist(new DynamoDBClient({}), { id: "petsByOwner" });
expose(app, { id: "pet-api" });
```

```console
$ cloudcc ./app
cloudcc: compiled petstore into compiled
```

**Every call here is inert.** The compiler reads them statically — it never
imports or runs your program — and rewrites them, in a *copy* of your source,
into clients pointed at real cloud resources. Uncompiled, `persist` returns
exactly what you passed it, so your program talks to a local DynamoDB, a local
Redis, a local Postgres, and runs with no cloud account.

This package never imports the AWS SDK. That only appears in the
`_cloudcc_runtime` module the compiler injects into the compiled copy.

## What each call becomes

| Call | Compiles to |
|---|---|
| `expose(app, { id })` | API Gateway v2, or an ALB for a container unit |
| `executionUnit({ id })` | Lambda, or ECS Fargate, or a Kubernetes Deployment |
| `persist(client, { id })` | the client you passed decides — see below |
| `staticUnit(id, { staticFiles })` | an S3 website, or CloudFront in front of one |
| `configValue(id, { secret })` | an environment variable, or a Pulumi stack secret |
| `embedAssets(pattern)` | files bundled with the declaring unit |
| `remote(module, { id })` | a Lambda invoke, in place of an in-process call |

`persist` is type-preserving — it returns exactly what you gave it, so the type
your editor shows is the type you keep:

| What you pass | Compiles to |
|---|---|
| `new DynamoDBClient({})` | DynamoDB |
| `new S3Client({})` | S3 |
| `new Pool({ connectionString })` (pg) | RDS |
| `knex({ client: "pg", connection })` | RDS |
| `new Redis({ host })` (ioredis) | ElastiCache |
| `createClient({ url })` (node-redis) | ElastiCache |
| `new Topic()` | SNS, or SQS for a single subscriber |
| `new Secret()` | Secrets Manager |

Two rules follow from the hints being *read* rather than run:

* **Arguments must be literals.** `persist(client, { id: name })` is a compile
  error pointing at the argument, because the compiler would have to run your
  program to know what `name` is.
* **Ids are explicit**, never inferred from the variable you assign to:
  renaming a local would otherwise replace a live resource.

## Running your program

The SDK is ESM and needs **Node 22.12 or newer**, which is where `require()` of
an ES module became available — so a CommonJS program can `require` it too:

```js
const { persist, expose } = require("@cloudcompiler/sdk");
```

TypeScript needs something that can run TypeScript, exactly as a Python program
needs uvicorn:

```bash
npx tsx server.ts        # or: node --import tsx server.ts
```

Nothing the compiler *produces* needs `tsx`: a unit is bundled by esbuild on
the way out, so what gets deployed is plain JavaScript either way.

## Seeing what it did

```bash
CLOUDCC_TRACE=1 npx tsx server.ts 2> trace.log
cloudcc trace trace.log
```

Records every call made through a persisted client, under the id you gave it.
Off unless the variable is set.

## Further reading

- [Every capability, in TypeScript](../../docs/typescript.md)
- [The main README](../../README.md)
