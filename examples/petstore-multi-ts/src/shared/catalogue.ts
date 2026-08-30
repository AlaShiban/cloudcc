// The relational half, and the cache in front of it.
//
// Two more capabilities, declared the same way as everything else: by wrapping
// the client the program already uses. A Knex instance pointed at Postgres asks
// for RDS Postgres; a node-redis client asks for ElastiCache. Neither is a
// class of cloudcc's, so every query builder method and every type Knex ships
// still applies.
//
// **Both clients here are built by a factory, and that is the point.**
// `knex(...)` and `createClient(...)` are calls, not constructors -- so unlike
// `new DynamoDBClient()` the compiler cannot name their type, and the rewritten
// line would be `const db = _cloudccOrm.connect("petsdb")` with no type at all.
// The annotation on the binding is what fixes it: rewriting replaces the
// `persist(...)` call and nothing else, so `const db: Knex =` survives into the
// compiled copy and the type comes back. Annotating a factory-built store is
// the TypeScript habit worth having.
//
// Imported by the api unit alone. The worker has no business reading the
// catalogue, and because permissions are derived from what a unit bundles, not
// importing it is how the worker ends up without them.
//
// Uncompiled this talks to a local Postgres and a local Redis:
//
//     docker run -d -p 5432:5432 -e POSTGRES_USER=ccadmin \
//       -e POSTGRES_DB=petsdb -e POSTGRES_HOST_AUTH_METHOD=trust postgres:16-alpine
//     docker run -d -p 6379:6379 redis:7-alpine

import { persist } from "@cloudcompiler/sdk";
import knex, { type Knex } from "knex";
import { createClient, type RedisClientType } from "redis";

/** One breed, and how often it has been seen. */
export interface Breed {
  name: string;
  species: string;
  seen: number;
}

// The URL is the local one, and it carries no password because the compiled
// form has none either: AWS manages the master credential and the shim splices
// it in from the managed secret, so nothing sensitive is ever an environment
// variable.
const db: Knex = persist(
  knex({
    client: "pg",
    connection: "postgresql://ccadmin@localhost:5432/petsdb",
  }),
  { id: "petsdb", models: ["Breed"] },
);

const cache: RedisClientType = persist(
  createClient({ url: "redis://localhost:6379" }),
  { id: "petCache" },
);

/** How long a cached summary stays fresh. */
const CACHE_SECONDS = 300;

// node-redis has to be connected before it is used, and `connect()` returns a
// promise the shim deliberately does not await for you: a synchronous
// `connect()` is what keeps the compiled binding the same shape as the
// uncompiled one. Commands issued before it resolves are queued, so one
// memoised call at first use is all this needs.
let connected: Promise<unknown> | null = null;
function ready(): Promise<unknown> {
  connected ??= cache.connect().catch(() => undefined);
  return connected;
}

let schemaReady: Promise<unknown> | null = null;

/** Create the breeds table if it is not there. */
function ensureSchema(): Promise<unknown> {
  schemaReady ??= db.schema.hasTable("breeds").then(async (exists: boolean) => {
    if (!exists) {
      await db.schema.createTable("breeds", (t) => {
        t.text("name").primary();
        t.text("species").notNullable().defaultTo("unknown");
        t.integer("seen").notNullable().defaultTo(0);
      });
    }
  });
  return schemaReady;
}

/** Count one sighting of a breed, and return the new total. */
export async function recordSighting(breed: string, species: string): Promise<number> {
  await ensureSchema();
  const rows = await db("breeds")
    .insert({ name: breed, species, seen: 1 })
    .onConflict("name")
    .merge({ seen: db.raw("breeds.seen + 1") })
    .returning<{ seen: number }[]>("seen");
  return Number(rows[0].seen);
}

export async function breeds(): Promise<Breed[]> {
  await ensureSchema();
  return db<Breed>("breeds").select("name", "species", "seen").orderBy("name");
}

export async function cacheSummary(petId: string, summary: string): Promise<void> {
  await ready();
  await cache.setEx(`pet:${petId}`, CACHE_SECONDS, summary);
}

export async function cachedSummary(petId: string): Promise<string | null> {
  await ready();
  return cache.get(`pet:${petId}`);
}

export async function forget(petId: string): Promise<void> {
  await ready();
  await cache.del(`pet:${petId}`);
}
