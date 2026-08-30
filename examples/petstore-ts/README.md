# TypeScript, on both kinds of compute

Two units, one language, two shapes of compute:

* `src/server.ts` is the HTTP unit, compiled to an **ECS Fargate container**
  behind an application load balancer.
* `src/summary.ts` is a **Lambda function** the container calls.

It exists because of what nothing else covers. Until this example there was no
TypeScript application that had ever been *run* — `.ts` reached the compiler,
produced an IR, and stopped there — and no Node container unit at all, in either
language. Both were verified once by hand and never again.

## What it exercises that the other examples do not

**TypeScript that cannot be run by `node`.** `node src/server.ts` is not a
thing. Uncompiled, this program needs a loader, exactly as a Python one needs
uvicorn:

```bash
docker run -p 8000:8000 amazon/dynamodb-local
CLOUDCC_AWS_ENDPOINT_URL=http://localhost:8000 npx tsx src/server.ts
```

Compiled, it needs nothing: esbuild strips the types on the way into the bundle,
so what deploys is plain JavaScript whichever language the unit was written in.

**A path alias.** `@/store` is a `paths` entry in `tsconfig.json`, which is how
most TypeScript projects of any size import their own code. `cloudcc` reads the
same file the type checker does, so the module lands in the unit's bundle — and
an alias that maps to nothing is a compile error naming the tsconfig, rather
than a bundler failure with no idea why.

**Type syntax around the hints.** `new DynamoDBClient({}) as DynamoDBClient`,
`{ id: "petsByOwner" as const }`, `expose(app!, …)`. All three are ordinary
TypeScript, all three are erased before the program runs, and all three used to
stop the compiler recognising what it was being handed.

**A type-only import that stays out of the bundle.** `summary.ts` imports
`type { Pet }` from the store. Every TypeScript build erases that statement, so
the store is *not* in the summary unit — which is why the summary function has
no table binding and no DynamoDB permissions. Following the import anyway would
have granted it both.

**A container that starts.** A container runs the program rather than a handler
the platform calls, so "can `node` load this?" is a real question there in a way
it is not for a function. `cloudcc` bundles a container the same way it bundles
a function, and the image carries one `index.mjs`.

## Two units, in TypeScript

`remote(summaryModule, { id: "summary" })` is the seam. Uncompiled it is the
module imported directly above it, so the whole application runs in one process
and `npx tsx src/server.ts` serves every route. Compiled, that import is gone,
`summary.ts` is not in the container's bundle, and the same `await` is a Lambda
invocation from inside the task.

That is also what makes the differential test mean something here: every
`GET /pets/{id}` in the compiled half crosses from the container into the
deployed function, so a bundle that failed to load could not produce a matching
response.

## What is configuration and what is code

```yaml
execution_units:
  web:
    type: container
exposed:
  pet-api:
    type: alb
```

Nothing in `src/` says container, Fargate, or load balancer. Moving this unit to
a function is those two entries, and the program does not change.
