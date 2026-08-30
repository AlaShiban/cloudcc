// The HTTP-facing execution unit, in TypeScript.
//
// It holds four kinds of state, each declared by wrapping the client that
// provides it, and each becoming a different AWS service once compiled:
//
//     a DynamoDBClient       -> DynamoDB      the pets themselves
//     a Knex instance        -> RDS Postgres  the breed catalogue
//     a node-redis client    -> ElastiCache   a cache in front of the reads
//     a cloudcc.Topic        -> SQS           what the worker reacts to
//       (one subscriber)
//
// Two of those are built by a *factory* rather than a constructor, which is
// the difference TypeScript notices: see the note in shared/catalogue.ts about
// why those two carry a type annotation and these do not need one.
//
// Nothing below says any of those service names. The client's type decides the
// capability and cloudcc.yaml chooses between variants of it, so moving the
// cache to MemoryDB stays a configuration change rather than a code change.

import express, { type NextFunction, type Request, type RequestHandler, type Response } from "express";
import { configValue, executionUnit, expose, persist, staticUnit } from "@cloudcompiler/sdk";

import {
  breeds,
  cacheSummary,
  cachedSummary,
  forget,
  recordSighting,
} from "@/shared/catalogue";
import {
  deletePet as dropPet,
  events,
  readPet,
  summarize,
  writePet,
  type Pet,
} from "@/shared/store";

executionUnit({ id: "api" });

// Claimed before execution-unit closure runs, so these assets never end up
// inside a Lambda bundle.
// The glob is relative to *this* file, and the assets sit beside src/.
staticUnit("petstore-ts-site", { staticFiles: "../public/**/*", indexDocument: "index.html" });

const app = express();
app.use(express.json());
expose(app, { id: "pet-api" });

const logLevel = configValue("log_level", { default: "info" });

// Express 4 does not catch a rejected promise from an async handler: the
// rejection goes unhandled and Node exits the process. Forwarding it to the
// error handler is what a synchronous `throw` would have done.
type Handler = (req: Request, res: Response) => Promise<void> | void;

const route =
  (handler: Handler): RequestHandler =>
  (req, res, next) =>
    Promise.resolve(handler(req, res)).catch(next);

app.get(
  "/health",
  route((_req, res) => {
    res.json({ status: "ok", log_level: logLevel });
  }),
);

app.get(
  "/pets/:petId",
  route(async (req, res) => {
    const pet = await readPet(req.params.petId);
    if (!pet) {
      res.status(404).json({ detail: "no such pet" });
      return;
    }
    // The cache is consulted for the summary only. The pet itself comes from
    // the table either way, so a stale cache costs a description rather than a
    // wrong record.
    let summary = await cachedSummary(req.params.petId);
    let cached = true;
    if (summary === null) {
      summary = summarize(pet);
      await cacheSummary(req.params.petId, summary);
      cached = false;
    }
    res.json({ ...pet, summary, cached });
  }),
);

app.put(
  "/pets/:petId",
  route(async (req, res) => {
    const pet = req.body as Pet;
    await writePet(req.params.petId, pet);
    const summary = summarize(pet);
    await cacheSummary(req.params.petId, summary);
    const seen = await recordSighting(pet.breed ?? "unrecorded", pet.species ?? "unknown");
    await events.publish({ action: "created", id: req.params.petId, summary });
    res.json({ ok: true, id: req.params.petId, breed_seen: seen });
  }),
);

app.delete(
  "/pets/:petId",
  route(async (req, res) => {
    await dropPet(req.params.petId);
    await forget(req.params.petId);
    res.json({ ok: true });
  }),
);

/** The relational read path: a query rather than a key lookup. */
app.get(
  "/breeds",
  route(async (_req, res) => {
    res.json({ breeds: await breeds() });
  }),
);

app.use((err: any, req: Request, res: Response, next: NextFunction) => {
  console.error(`${req.method} ${req.path}: ${err?.name ?? "Error"}: ${err?.message ?? err}`);
  if (res.headersSent) {
    next(err);
    return;
  }
  res.status(500).json({ detail: "internal error" });
});

export { app };
