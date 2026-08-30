// Tracking: where an order is up to.
//
// The one unit that is reached both ways. It *subscribes* to courierAssigned,
// because a courier having been assigned is a statement nobody waits on, and it
// is *called* by the storefront, because a customer asking where their food is
// wants an answer now.
//
// Both arrive at the same Lambda and the generated entrypoint tells them apart
// by the shape of the event. Nothing in this file has to know that.

// Injected by cloudcc: runtime clients for this program's declared capabilities.
import * as _cloudccKv from "../../_cloudcc_runtime/kv.js";

import { DynamoDBClient, GetItemCommand, PutItemCommand } from "@aws-sdk/client-dynamodb";

import { courierAssigned, type CourierAssigned } from "./events";

undefined;

const TABLE = "nomnom-tracking" as const;

const timeline = _cloudccKv.connect("trackingEvents") as DynamoDBClient;

export interface OrderStatus {
  order_id: string;
  state: string;
  courier: string | null;
}

/**
 * A subscriber, not a remote function: nobody is waiting on the result.
 *
 * The compiler only requires `async` of functions something calls over the
 * wire, which this is not -- the message arrives on its own. It is `async` here
 * only because writing to DynamoDB is.
 */
async function onCourierAssigned(message: CourierAssigned) {
  const orderId = message.order_id ?? "unknown";
  await timeline.send(
    new PutItemCommand({
      TableName: TABLE,
      Item: {
        id: { S: orderId },
        state: { S: "out-for-delivery" },
        courier: { S: message.courier ?? "unassigned" },
      },
    }),
  );
  return { tracked: orderId };
}

courierAssigned.subscribe(onCourierAssigned);

/** Called by the storefront while a customer is looking at the page. */
export async function orderStatus(orderId: string): Promise<OrderStatus> {
  const out = await timeline.send(
    new GetItemCommand({ TableName: TABLE, Key: { id: { S: orderId } } }),
  );
  if (!out.Item) {
    return { order_id: orderId, state: "preparing", courier: null };
  }
  return {
    order_id: orderId,
    state: out.Item.state!.S!,
    courier: out.Item.courier?.S ?? null,
  };
}
