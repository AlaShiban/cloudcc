// A plain Express app. The only CloudCompiler-specific lines are the import
// and the two hint calls, which the compiler reads statically and rewrites
// into real AWS clients in the compiled copy.

import express from "express";
import { expose, persist, KVStore } from "@cloudcompiler/sdk";

const app = express();
app.use(express.json());

const pets = persist(new KVStore(), { id: "petsByOwner" });
expose(app, { id: "pet-api" });

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
