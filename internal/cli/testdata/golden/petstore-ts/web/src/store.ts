// The store, in a module the server reaches through a path alias.
//
// `@/store` is not a directory that exists: it is a `paths` entry in
// tsconfig.json, which is how most TypeScript projects of any size import their
// own code. cloudcc reads the same file the type checker does, so this module
// lands in the unit's bundle -- and an alias that maps to nothing is a compile
// error naming the tsconfig rather than a bundler failure ten minutes later.

// Injected by cloudcc: runtime clients for this program's declared capabilities.
import * as _cloudccKv from "../_cloudcc_runtime/kv.js";

import {
  DeleteItemCommand,
  DynamoDBClient,
  GetItemCommand,
  PutItemCommand,
  ScanCommand,
} from "@aws-sdk/client-dynamodb";

/** What a pet is, everywhere in this program. */
export interface Pet {
  name: string;
  species: string;
  age: number;
}

const TABLE = "pets" as const;

// `as const` on the id and a type assertion on the client: both are ordinary
// TypeScript, both are erased before the program runs, and both used to stop
// the compiler recognising what it was being handed.
const pets = _cloudccKv.connect("petsByOwner") as DynamoDBClient;

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

export async function listPets(): Promise<string[]> {
  const out = await pets.send(new ScanCommand({ TableName: TABLE }));
  return (out.Items ?? []).map((item) => item.id!.S!).sort();
}
