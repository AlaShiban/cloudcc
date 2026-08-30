// A container that serves HTTP, in TypeScript, and says nothing about how it
// is run.
//
// This is the whole example. There is no Kubernetes in it -- no manifest, no
// Deployment, no Service, no image reference -- because none of that is a
// property of the program. `cloudcc.yaml` says `platform: kubernetes` and the
// same file compiles to a Deployment behind a Service; remove that line and it
// compiles to a Fargate task behind a load balancer, with this file untouched.
//
// What TypeScript adds to that claim is the packaging: a container runs the
// program rather than a handler the platform calls, and `node web.ts` is not a
// thing -- so the unit is bundled by esbuild on the way into the image, exactly
// as a function is. The image carries one index.mjs whichever language it came
// from.
//
// It deliberately reaches no store. A pod's AWS identity comes from IRSA, which
// cloudcc does not emit yet, so a unit that did reach one would be warned at
// compile time -- and an example is a bad place to demonstrate a gap.
//
// Run it as written:
//
//     npx tsx src/web.ts

import express, { type Request, type Response } from "express";
import { executionUnit, expose } from "@cloudcompiler/sdk";

executionUnit({ id: "web" });

const app = express();
expose(app, { id: "web-front" });

app.get("/health", (_req: Request, res: Response) => {
  res.json({ status: "ok" });
});

/**
 * What the platform put around this process.
 *
 * Kubernetes sets HOSTNAME to the pod name, so this is a cheap way for a test
 * to tell a pod from a Fargate task without the application knowing which it
 * is running under.
 */
app.get("/where", (_req: Request, res: Response) => {
  res.json({ host: process.env.HOSTNAME ?? "unknown" });
});

// Exported, not listened to: the generated container entry is what starts the
// server, which is the same role uvicorn plays for a Python unit.
export { app };
