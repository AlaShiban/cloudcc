// The HTTP half of a two-language application.
//
// Nothing here is aware that the other unit is Python. Both units declare the
// same persist id, which is what makes them resolve to one DynamoDB table.
import express from "express";
import { KVStore, executionUnit, expose, persist } from "@cloudcompiler/sdk";

executionUnit({ id: "api" });

const pets = persist(new KVStore(), { id: "petsByOwner" });

const app = express();
app.use(express.json());
expose(app, { id: "pet-api" });

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
