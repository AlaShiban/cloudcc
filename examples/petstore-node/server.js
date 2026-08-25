// A plain Express app. The only CloudCompiler-specific lines are the import
// and the two hint calls, which the compiler reads statically and rewrites
// into real AWS clients in the compiled copy.
//
// The store is a `DynamoDBClient`. cloudcc supplies no class of its own for
// it: what `persist` hands back once compiled is a DynamoDBClient too, with
// every command it sends bound to the provisioned table -- so the table name
// written below is the local one, and the program never learns the other.
//
// Uncompiled, this needs a DynamoDB endpoint. A local one is fine:
//
//     docker run -p 8000:8000 amazon/dynamodb-local
//     export CLOUDCC_AWS_ENDPOINT_URL=http://localhost:8000

import {
  DeleteItemCommand,
  DynamoDBClient,
  GetItemCommand,
  PutItemCommand,
  ScanCommand,
} from "@aws-sdk/client-dynamodb";
import express from "express";
import { expose, persist } from "@cloudcompiler/sdk";

const app = express();
app.use(express.json());

const TABLE = "pets";
const pets = persist(new DynamoDBClient({}), { id: "petsByOwner" });
expose(app, { id: "pet-api" });

app.get("/health", (req, res) => {
  res.json({ status: "ok" });
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

app.put("/pets/:petId", async (req, res) => {
  await pets.send(
    new PutItemCommand({
      TableName: TABLE,
      // A JSON string rather than a nested map: DynamoDB's native number type
      // does not survive a round trip through JSON unchanged, and the Python
      // half of examples/mixed writes exactly this shape.
      Item: { id: { S: req.params.petId }, pet: { S: JSON.stringify(req.body) } },
    }),
  );
  res.json({ ok: true, id: req.params.petId });
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
