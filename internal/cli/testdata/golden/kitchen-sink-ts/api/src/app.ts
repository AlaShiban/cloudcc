// Exercises every capability cloudcc supports, in one TypeScript program.
//
// The api unit runs on Lambda behind an HTTP API; the reporter unit runs on
// Fargate behind an ALB. Both are plain TypeScript -- the only
// cloudcc-specific lines are the import and the hint calls.
//
// Two compute types from one language is the thing this example adds that
// nothing else does: the api is bundled into a zip Lambda loads, the reporter
// into an image that runs the program itself, and both come out of the same
// bundler with the same types stripped the same way.

// Injected by cloudcc: runtime clients for this program's declared capabilities.
import * as _cloudccConfig from "../_cloudcc_runtime/config.js";
import * as _cloudccExpose from "../_cloudcc_runtime/expose.js";

import express, { type NextFunction, type Request, type RequestHandler, type Response } from "express";

import {
  cache,
  events,
  readItem,
  signingKey,
  writeDoc,
  writeItem,
  databaseName,
} from "@/stores";

undefined;

const app = express();
app.use(express.json());
_cloudccExpose.register(app, { id: "shop-api" });

const logLevel = _cloudccConfig.value("log_level", { default: "info" });
const stripeKey = _cloudccConfig.value("stripe_key");

// Claimed so the seed data travels with this unit even though nothing imports
// it.
const SEED = "../data/*.json";
void SEED;
void stripeKey;

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
  "/items/:itemId",
  route(async (req, res) => {
    const cached = await cache.get(req.params.itemId);
    if (cached !== null) {
      res.json(JSON.parse(cached));
      return;
    }
    const item = await readItem(req.params.itemId);
    if (item === null) {
      res.status(404).json({ detail: "no such item" });
      return;
    }
    await cache.set(req.params.itemId, JSON.stringify(item), "EX", 60);
    res.json(item);
  }),
);

app.put(
  "/items/:itemId",
  route(async (req, res) => {
    await writeItem(req.params.itemId, req.body);
    await cache.del(req.params.itemId);
    await writeDoc(`${req.params.itemId}.json`, JSON.stringify(req.body));
    await events.publish({ action: "upserted", id: req.params.itemId });
    res.json({ ok: true, id: req.params.itemId });
  }),
);

app.get(
  "/receipt/:itemId",
  route(async (_req, res) => {
    const key = await signingKey.get();
    res.json({ signed_with: key.slice(0, 4), database: await databaseName() });
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
