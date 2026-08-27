// The HTTP half of a two-language application.
//
// Nothing here is aware that the other unit is Python. What the two units agree
// on is the *id* of each store; what each holds is whatever client its own
// ecosystem uses. Four stores, four client libraries here and three more --
// all different -- in worker.py:
//
//     id             this unit holds        worker.py holds        becomes
//     petsByOwner    a DynamoDBClient       a boto3 Table          DynamoDB
//     shopdb         a pg Pool              a SQLAlchemy engine    RDS Postgres
//     petPhotos      an S3Client            a pathlib.Path         S3
//     petCache       an ioredis client      --                     ElastiCache
//
// That table is the point of the example. `shopdb` is one database that a Node
// program reaches through pg and a Python program reaches through an ORM, and
// neither had to learn the other's client -- because cloudcc does not supply a
// client. It provisions what the client's type asked for and hands back one of
// the same kind.
import {
  DeleteItemCommand,
  DynamoDBClient,
  GetItemCommand,
  PutItemCommand,
  ScanCommand,
} from "@aws-sdk/client-dynamodb";
import { GetObjectCommand, PutObjectCommand, S3Client } from "@aws-sdk/client-s3";
import express from "express";
import Redis from "ioredis";
import { Pool } from "pg";
import { executionUnit, expose, persist } from "@cloudcompiler/sdk";

executionUnit({ id: "api" });

const TABLE = "pets";
const BUCKET = "pet-photos";
const pets = persist(new DynamoDBClient({}), { id: "petsByOwner" });

// A pg Pool asks for a relational database, and the URL it was given says which
// engine: `postgresql://` is RDS Postgres. The URL is the local one, and it
// carries no password because the compiled form has none either -- AWS manages
// the master credential and the shim supplies it lazily from the managed
// secret, so nothing sensitive is ever an environment variable.
const db = persist(
  new Pool({ connectionString: "postgresql://ccadmin@localhost:5432/shopdb" }),
  { id: "shopdb" },
);

// An ioredis client asks for ElastiCache. `cloudcc.yaml` is where that becomes
// MemoryDB instead, which is a configuration change rather than a code change.
const cache = persist(new Redis({ host: "localhost", port: 6379 }), { id: "petCache" });

// An S3Client is not bound to a bucket -- the name travels in each command --
// so the program keeps writing the name it chose and the shim rewrites it to
// the provisioned one. That is what lets this file run uncompiled against a
// local S3 and compiled against a real one without changing a line.
const photos = persist(new S3Client({}), { id: "petPhotos" });

// Express 4 does not catch a rejected promise from an async handler: the
// rejection goes unhandled and Node exits the process. One transient error from
// any store below would therefore take the whole unit down, which under load is
// the difference between a slow request and a dead server.
//
// `route` is the ordinary remedy -- forward the rejection to Express's error
// handler, which is what a synchronous `throw` would have done. It is the
// application's business rather than cloudcc's: nothing here is compiler
// machinery, and the same wrapper would be in this file if the stores were
// local.
const route = (handler) => (req, res, next) => Promise.resolve(handler(req, res)).catch(next);

const app = express();
app.use(express.json());
expose(app, { id: "pet-api" });

/** How long a cached pet stays fresh. Short, because it is a read-through. */
const CACHE_SECONDS = 300;

/** Create the sightings table if it is not there.
 *
 * A real service would run a migration at deploy time. An example gets to do
 * the simple thing, and `IF NOT EXISTS` is idempotent.
 *
 * The promise is memoised, and not merely as an optimisation. `IF NOT EXISTS`
 * is idempotent but not concurrency-safe: two connections running it at the
 * same moment race between the catalogue check and the create, and one of them
 * gets `duplicate key value violates unique constraint "pg_type_typname_nsp_index"`.
 * Under eight concurrent sessions that is a handful of 500s on the first
 * second of every run. Awaiting one shared promise means it happens once.
 */
let schemaReady = null;
function ensureSchema() {
  schemaReady ??= db.query(`
    CREATE TABLE IF NOT EXISTS sightings (
      breed TEXT PRIMARY KEY,
      species TEXT NOT NULL DEFAULT 'unknown',
      seen INTEGER NOT NULL DEFAULT 0
    )
  `);
  return schemaReady;
}

app.get("/health", route(async (req, res) => {
  res.json({ status: "ok" });
}));

app.put("/pets/:petId", route(async (req, res) => {
  await pets.send(
    new PutItemCommand({
      TableName: TABLE,
      Item: { id: { S: req.params.petId }, pet: { S: JSON.stringify(req.body) } },
    }),
  );
  // Writing a pet invalidates whatever the cache remembered about it. Doing
  // this rather than letting the entry expire is what makes a stale read a bug
  // the load test can actually catch.
  await cache.del(`pet:${req.params.petId}`);
  res.json({ ok: true, id: req.params.petId });
}));

app.get("/pets/:petId", route(async (req, res) => {
  const key = `pet:${req.params.petId}`;
  const hit = await cache.get(key);
  if (hit !== null) {
    res.json({ ...JSON.parse(hit), cached: true });
    return;
  }
  const out = await pets.send(
    new GetItemCommand({ TableName: TABLE, Key: { id: { S: req.params.petId } } }),
  );
  if (!out.Item) {
    res.status(404).json({ detail: "no such pet" });
    return;
  }
  const pet = JSON.parse(out.Item.pet.S);
  await cache.setex(key, CACHE_SECONDS, JSON.stringify(pet));
  res.json({ ...pet, cached: false });
}));

app.delete("/pets/:petId", route(async (req, res) => {
  await pets.send(
    new DeleteItemCommand({ TableName: TABLE, Key: { id: { S: req.params.petId } } }),
  );
  await cache.del(`pet:${req.params.petId}`);
  res.json({ ok: true });
}));

app.get("/pets", route(async (req, res) => {
  const page = await pets.send(
    new ScanCommand({ TableName: TABLE, ProjectionExpression: "id" }),
  );
  res.json({ keys: (page.Items ?? []).map((item) => item.id.S).sort() });
}));

// --- the relational half ---------------------------------------------------

app.post("/sightings/:breed", route(async (req, res) => {
  await ensureSchema();
  const species = req.body?.species ?? "unknown";
  const out = await db.query(
    `INSERT INTO sightings (breed, species, seen) VALUES ($1, $2, 1)
       ON CONFLICT (breed) DO UPDATE SET seen = sightings.seen + 1
       RETURNING seen`,
    [req.params.breed, species],
  );
  res.json({ breed: req.params.breed, seen: out.rows[0].seen });
}));

app.get("/sightings", route(async (req, res) => {
  await ensureSchema();
  const out = await db.query("SELECT breed, species, seen FROM sightings ORDER BY breed");
  res.json({ sightings: out.rows });
}));

// --- the bucket ------------------------------------------------------------

app.put("/photos/:petId", route(async (req, res) => {
  await photos.send(
    new PutObjectCommand({
      Bucket: BUCKET,
      Key: `${req.params.petId}.json`,
      Body: JSON.stringify(req.body ?? {}),
      ContentType: "application/json",
    }),
  );
  res.json({ ok: true, key: `${req.params.petId}.json` });
}));

app.get("/photos/:petId", route(async (req, res) => {
  try {
    const out = await photos.send(
      new GetObjectCommand({ Bucket: BUCKET, Key: `${req.params.petId}.json` }),
    );
    res.type("application/json").send(await out.Body.transformToString());
  } catch (err) {
    if (err?.name === "NoSuchKey" || err?.$metadata?.httpStatusCode === 404) {
      res.status(404).json({ detail: "no such photo" });
      return;
    }
    // Anything else is genuinely unexpected, and `route` above hands it to the
    // error handler rather than letting it end the process.
    throw err;
  }
}));

// The other half of `route`. Without a handler of four arguments Express uses
// its default one, which is fine for a 500 but says nothing in the log about
// which store failed -- and this application has four of them.
app.use((err, req, res, next) => {
  console.error(`${req.method} ${req.path}: ${err?.name ?? "Error"}: ${err?.message ?? err}`);
  if (res.headersSent) {
    next(err);
    return;
  }
  res.status(500).json({ detail: "internal error" });
});

export { app };
