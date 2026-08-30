# Mixed-language example

One application, two units, two languages, four stores — and eight different
client libraries between them.

* `api.ts` is a TypeScript HTTP unit, compiled to Lambda behind API Gateway v2.
* `worker.py` is a Python unit with no HTTP surface.

Three of the four stores are declared by *both* units, under the same id, with
whatever client each ecosystem actually uses:

| id | `api.ts` holds | `worker.py` holds | becomes |
|---|---|---|---|
| `petsByOwner` | a `DynamoDBClient` | a boto3 `Table` | DynamoDB |
| `shopdb` | a `pg` `Pool` | a SQLAlchemy engine | RDS Postgres |
| `petPhotos` | an `S3Client` | a `pathlib.Path` | S3 |
| `petCache` | an `ioredis` client | — | ElastiCache |

That table is the point of the example. `shopdb` is **one** Postgres instance
that a Node program reaches through a connection pool and a Python program
reaches through an ORM, and neither file mentions RDS. The client's *type* says
what to provision; the id says which one; and what comes back is a client of the
same kind the source constructed — a real `Pool`, a real `Engine`, a real
`S3Path` — so every method, option and type stub those libraries ship still
applies.

Nothing in `cloudcc.yaml` mentions a language, and nothing mentions a service. A
frontend is chosen per execution unit from its entrypoint's extension, and
moving the cache to MemoryDB is a line of configuration rather than a code
change.

```console
$ cloudcc examples/mixed -o compiled
```

## Running it uncompiled

Both units run as ordinary programs against local servers, which is the check
that the source is not written against cloudcc:

```console
$ docker run -d -p 5432:5432 -e POSTGRES_USER=ccadmin -e POSTGRES_DB=shopdb \
    -e POSTGRES_HOST_AUTH_METHOD=trust postgres:16-alpine
$ docker run -d -p 6379:6379 redis:7-alpine
```

## Routes

`/health`, `/pets`, `/pets/{id}` (GET/PUT/DELETE) — DynamoDB, read through the
cache. A write invalidates the cached entry rather than waiting for it to
expire, so a stale read is a bug a test can actually catch.

`/sightings`, `/sightings/{breed}` (POST) — the relational half. The same rows
`worker.py` reads back through `Sighting`.

`/photos/{id}` (GET/PUT) — the bucket, which `worker.py` also writes to.
