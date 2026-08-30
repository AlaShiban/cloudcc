// Shared by both execution units.
//
// The same persisted id is referenced from one module that both units import,
// so the two units end up wired to a single DynamoDB table with their own
// environment bindings. This is the multi-unit case in TypeScript: what makes
// them one table is the id, not the file.

// Injected by cloudcc: runtime clients for this program's declared capabilities.
import * as _cloudccKv from "../../_cloudcc_runtime/kv.js";
import * as _cloudccPubsub from "../../_cloudcc_runtime/pubsub.js";

import {
  DeleteItemCommand,
  DynamoDBClient,
  GetItemCommand,
  PutItemCommand,
} from "@aws-sdk/client-dynamodb";

/** What a pet is, everywhere in this program. */
export interface Pet {
  name: string;
  species?: string;
  breed?: string;
}

const TABLE = "pets" as const;

const pets = _cloudccKv.connect("petsByOwner") as DynamoDBClient;

//: One subscriber -- the worker, and nothing else listens -- so this is a queue
//: rather than a fan-out, and the compiler resolves it to SQS. Nothing here
//: says SQS: what is declared is the requirement.
export const events = _cloudccPubsub.connect("petEvents");

export function summarize(pet: Pet): string {
  return `${pet.name ?? "unnamed"} (${pet.species ?? "unknown"})`;
}

/** Both units read the table the same way, so the shape lives here. */
export async function readPet(id: string): Promise<Pet | null> {
  const out = await pets.send(
    new GetItemCommand({ TableName: TABLE, Key: { id: { S: id } } }),
  );
  return out.Item ? (JSON.parse(out.Item.pet!.S!) as Pet) : null;
}

export async function writePet(id: string, pet: Pet): Promise<void> {
  await pets.send(
    new PutItemCommand({
      TableName: TABLE,
      Item: { id: { S: id }, pet: { S: JSON.stringify(pet) } },
    }),
  );
}

export async function deletePet(id: string): Promise<void> {
  await pets.send(new DeleteItemCommand({ TableName: TABLE, Key: { id: { S: id } } }));
}
