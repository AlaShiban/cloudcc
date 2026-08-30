// A plain Express app in TypeScript, compiled to a container behind a load
// balancer rather than to a function behind an API gateway.
//
// Nothing below says which. `type: container` in cloudcc.yaml is the whole
// difference, and it is the reason this example exists: a container runs the
// program itself rather than a handler the platform calls, so it is the path
// where "can node load this file?" is a real question. It cannot -- `node
// server.ts` is not a thing -- which is why cloudcc bundles a container the
// same way it bundles a function.
//
// Run it uncompiled the way you would run any TypeScript program:
//
//     docker run -p 8000:8000 amazon/dynamodb-local
//     CLOUDCC_AWS_ENDPOINT_URL=http://localhost:8000 npx tsx src/server.ts

// Injected by cloudcc: runtime clients for this program's declared capabilities.
import * as _cloudccExpose from "../_cloudcc_runtime/expose.js";
import * as _cloudccRpc from "../_cloudcc_runtime/rpc.js";

import express, { type Request, type Response } from "express";

import { deletePet, listPets, readPet, writePet, type Pet } from "@/store";

undefined;

// The seam between the two units. Uncompiled this is the module imported above;
// compiled, that import is removed, summary.ts is not in this bundle, and the
// await becomes a Lambda invocation from inside the container.
const summary = _cloudccRpc.connect("summary");

const app = express();
app.use(express.json());

// `app!` is a non-null assertion: idiomatic TypeScript, erased before the
// program runs, and the compiler has to see through it to know which binding
// holds the application -- otherwise the routes below are never discovered.
_cloudccExpose.register(app, { id: "pet-api" });

app.get("/health", (_req: Request, res: Response) => {
  res.json({ status: "ok" });
});

app.get("/pets", async (_req: Request, res: Response) => {
  res.json({ pets: await listPets() });
});

app.get("/pets/:petId", async (req: Request, res: Response) => {
  const pet = await readPet(req.params.petId);
  if (!pet) {
    res.status(404).json({ detail: "no such pet" });
    return;
  }
  res.json({ ...pet, summary: await summary.describe(pet) });
});

app.put("/pets/:petId", async (req: Request, res: Response) => {
  const pet = req.body as Pet;
  await writePet(req.params.petId, pet);
  res.json({ ok: true, id: req.params.petId, summary: await summary.describe(pet) });
});

app.delete("/pets/:petId", async (req: Request, res: Response) => {
  await deletePet(req.params.petId);
  res.json({ ok: true });
});

// Exported, not listened to. A module that called listen() on import could not
// also be wrapped by a Lambda handler, so the generated container entry is what
// starts the server -- the same role uvicorn plays for a Python unit.
export { app };
