// The HTTP half of a two-language application.
//
// Nothing here is aware that the other unit is Python. Both units declare the
// same persist id, which is what makes them resolve to one DynamoDB table.
//
// What each language holds is its own AWS client -- a DynamoDBClient here, a
// boto3 Table in worker.py -- and what they agree on is the item shape, which
// is written out explicitly on both sides rather than hidden inside a class
// cloudcc supplies.
import {
  DeleteItemCommand,
  DynamoDBClient,
  GetItemCommand,
  PutItemCommand,
  ScanCommand,
} from "@aws-sdk/client-dynamodb";
import express from "express";
import { executionUnit, expose, persist } from "@cloudcompiler/sdk";

executionUnit({ id: "api" });

const TABLE = "pets";
const pets = persist(new DynamoDBClient({}), { id: "petsByOwner" });

const app = express();
app.use(express.json());
expose(app, { id: "pet-api" });

app.get("/health", async (req, res) => {
  res.json({ status: "ok" });
});

app.put("/pets/:petId", async (req, res) => {
  await pets.send(
    new PutItemCommand({
      TableName: TABLE,
      Item: { id: { S: req.params.petId }, pet: { S: JSON.stringify(req.body) } },
    }),
  );
  res.json({ ok: true, id: req.params.petId });
});

app.get("/pets/:petId", async (req, res) => {
  const out = await pets.send(
    new GetItemCommand({ TableName: TABLE, Key: { id: { S: req.params.petId } } }),
  );
  if (!out.Item) {
    res.status(404).json({ detail: "no such pet" });
    return;
  }
  res.json(JSON.parse(out.Item.pet.S));
});

app.delete("/pets/:petId", async (req, res) => {
  await pets.send(
    new DeleteItemCommand({ TableName: TABLE, Key: { id: { S: req.params.petId } } }),
  );
  res.json({ ok: true });
});

app.get("/pets", async (req, res) => {
  const page = await pets.send(
    new ScanCommand({ TableName: TABLE, ProjectionExpression: "id" }),
  );
  res.json({ keys: (page.Items ?? []).map((item) => item.id.S).sort() });
});

export { app };
