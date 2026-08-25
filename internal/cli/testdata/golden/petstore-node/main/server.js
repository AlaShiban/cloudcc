// A plain Express app. The only CloudCompiler-specific lines are the import
// and the two hint calls, which the compiler reads statically and rewrites
// into real AWS clients in the compiled copy.

// Injected by cloudcc: runtime clients for this program's declared capabilities.
import * as _cloudccExpose from "./_cloudcc_runtime/expose.js";
import * as _cloudccKv from "./_cloudcc_runtime/kv.js";

import express from "express";

const app = express();
app.use(express.json());

const pets = _cloudccKv.connect("petsByOwner");
_cloudccExpose.register(app, { id: "pet-api" });

app.get("/health", (req, res) => {
  res.json({ status: "ok" });
});

app.get("/pets/:petId", async (req, res) => {
  const pet = await pets.get(req.params.petId);
  if (pet === null) {
    res.status(404).json({ detail: "no such pet" });
    return;
  }
  res.json(pet);
});

app.put("/pets/:petId", async (req, res) => {
  await pets.put(req.params.petId, req.body);
  res.json({ ok: true, id: req.params.petId });
});

app.delete("/pets/:petId", async (req, res) => {
  await pets.delete(req.params.petId);
  res.json({ ok: true });
});

app.get("/pets", async (req, res) => {
  res.json({ keys: await pets.keys() });
});

export { app };
