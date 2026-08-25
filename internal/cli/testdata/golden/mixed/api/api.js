// The HTTP half of a two-language application.
//
// Nothing here is aware that the other unit is Python. Both units declare the
// same persist id, which is what makes them resolve to one DynamoDB table.
// Injected by cloudcc: runtime clients for this program's declared capabilities.
import * as _cloudccExpose from "./_cloudcc_runtime/expose.js";
import * as _cloudccKv from "./_cloudcc_runtime/kv.js";

import express from "express";

undefined;

const pets = _cloudccKv.connect("petsByOwner");

const app = express();
app.use(express.json());
_cloudccExpose.register(app, { id: "pet-api" });

app.get("/health", async (req, res) => {
  res.json({ status: "ok" });
});

app.put("/pets/:petId", async (req, res) => {
  await pets.put(req.params.petId, req.body);
  res.json({ ok: true, id: req.params.petId });
});

app.get("/pets/:petId", async (req, res) => {
  const found = await pets.get(req.params.petId);
  if (found === null || found === undefined) {
    res.status(404).json({ detail: "no such pet" });
    return;
  }
  res.json(found);
});

app.delete("/pets/:petId", async (req, res) => {
  await pets.delete(req.params.petId);
  res.json({ ok: true });
});

app.get("/pets", async (req, res) => {
  res.json({ keys: (await pets.keys()).sort() });
});

export { app };
