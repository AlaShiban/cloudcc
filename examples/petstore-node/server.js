// A plain Express app. The only CloudCompiler-specific lines are the import
// and the two hint calls, which the compiler reads statically and rewrites
// into real AWS clients in the compiled copy.

import express from "express";
import { expose, persistKv } from "@cloudcompiler/sdk";

const app = express();
app.use(express.json());

const pets = persistKv("petsByOwner");
expose(app, { id: "pet-api" });

app.get("/health", (req, res) => {
  res.json({ status: "ok" });
});

app.get("/pets/:petId", (req, res) => {
  const pet = pets.get(req.params.petId);
  if (pet === null) {
    res.status(404).json({ detail: "no such pet" });
    return;
  }
  res.json(pet);
});

app.put("/pets/:petId", (req, res) => {
  pets.put(req.params.petId, req.body);
  res.json({ ok: true, id: req.params.petId });
});

app.delete("/pets/:petId", (req, res) => {
  pets.delete(req.params.petId);
  res.json({ ok: true });
});

app.get("/pets", (req, res) => {
  res.json({ keys: pets.keys() });
});

export { app };
