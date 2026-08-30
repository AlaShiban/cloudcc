// A container-hosted unit: it serves a summary of what the others have done.
//
// The same source that becomes a Lambda zip in app.ts becomes an image here,
// and the only thing that says so is `type: container` in cloudcc.yaml.
//
// It reads two stores and answers one route, which is all a container can be
// here. Reacting to the topic is auditor.ts's job: a delivery is pushed to a
// function, and nothing polls on a container's behalf, so a container that
// subscribed would be a handler nothing ever called.

// Injected by cloudcc: runtime clients for this program's declared capabilities.
import * as _cloudccExpose from "../_cloudcc_runtime/expose.js";

import express, { type Request, type Response } from "express";

import { countDocs, countItems } from "@/stores";

undefined;

const app = express();
_cloudccExpose.register(app, { id: "reporter-web" });

app.get("/health", (_req: Request, res: Response) => {
  res.json({ status: "ok" });
});

app.get("/summary", (_req: Request, res: Response) => {
  void (async () => {
    res.json({ items: await countItems(), documents: await countDocs() });
  })();
});

export { app };
