// Inventory: holding stock while an order is being placed.
//
// Called by two units for different reasons -- the storefront reserves during
// checkout, dispatch confirms once a courier is on the way -- which is what
// makes it worth being a unit rather than a module. Both callers wait for the
// answer, because "did the reservation succeed" is not something either can
// carry on without.

// Injected by cloudcc: runtime clients for this program's declared capabilities.
import * as _cloudccKv from "../../_cloudcc_runtime/kv.js";

import {
  DeleteItemCommand,
  DynamoDBClient,
  GetItemCommand,
  PutItemCommand,
  UpdateItemCommand,
} from "@aws-sdk/client-dynamodb";

import { describe, type BasketItem } from "./menu";

undefined;

const TABLE = "nomnom-reservations" as const;

const reservations = _cloudccKv.connect("reservations") as DynamoDBClient;

export interface Reservation {
  order_id: string;
  state: string;
}

/** Hold stock for an order, and say whether it worked. */
export async function reserve(orderId: string, items: BasketItem[]): Promise<Reservation> {
  await reservations.send(
    new PutItemCommand({
      TableName: TABLE,
      Item: {
        id: { S: orderId },
        state: { S: "held" },
        items: { S: JSON.stringify(items) },
        summary: { S: describe(items) },
      },
    }),
  );
  return { order_id: orderId, state: "held" };
}

/** Turn a hold into a commitment. Called by dispatch. */
export async function confirm(orderId: string): Promise<Reservation> {
  await reservations.send(
    new UpdateItemCommand({
      TableName: TABLE,
      Key: { id: { S: orderId } },
      UpdateExpression: "SET #s = :s",
      ExpressionAttributeNames: { "#s": "state" },
      ExpressionAttributeValues: { ":s": { S: "committed" } },
    }),
  );
  return { order_id: orderId, state: "committed" };
}

/** Give the stock back. */
export async function release(orderId: string): Promise<Reservation> {
  await reservations.send(
    new DeleteItemCommand({ TableName: TABLE, Key: { id: { S: orderId } } }),
  );
  return { order_id: orderId, state: "released" };
}

export async function stateOf(orderId: string): Promise<Reservation> {
  const out = await reservations.send(
    new GetItemCommand({ TableName: TABLE, Key: { id: { S: orderId } } }),
  );
  return { order_id: orderId, state: out.Item ? out.Item.state!.S! : "none" };
}
