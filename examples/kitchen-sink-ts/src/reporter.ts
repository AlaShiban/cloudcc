// A container-hosted unit: it serves a summary of what the others have done.
//
// The same source that becomes a Lambda zip in app.ts becomes an image here,
// and the only thing that says so is `type: container` in cloudcc.yaml.
//
// It reads two stores and answers one route, which is all a container can be
// here. Reacting to the topic is auditor.ts's job: a delivery is pushed to a
// function, and nothing polls on a container's behalf, so a container that
// subscribed would be a handler nothing ever called.

import express, { type Request, type Response } from "express";
import { executionUnit, expose } from "@cloudcompiler/sdk";

import { countDocs, countItems } from "@/stores";

executionUnit({ id: "reporter" });

const app = express();
expose(app, { id: "reporter-web" });

app.get("/health", (_req: Request, res: Response) => {
  res.json({ status: "ok" });
});

app.get("/summary", (_req: Request, res: Response) => {
  void (async () => {
    res.json({ items: await countItems(), documents: await countDocs() });
  })();
});

export { app };
