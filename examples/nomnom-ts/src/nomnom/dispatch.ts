// Dispatch: finding a courier once an order has been placed.
//
// The unit that does both things in one direction. It is woken by a *message*
// (orderPlaced), and while handling it makes a *call* (inventory.confirm) and
// publishes another message (courierAssigned).
//
// That mix is deliberate. Confirming the reservation is a question -- if it
// fails the order should not be dispatched -- so it is a call and dispatch
// waits. Telling everyone a courier is on the way is a statement, so it is a
// message and dispatch does not.

import { DynamoDBClient, PutItemCommand } from "@aws-sdk/client-dynamodb";
import { executionUnit, persist, remote } from "@cloudcompiler/sdk";

import * as inventoryModule from "./inventory";
import { courierAssigned, orderPlaced, type OrderPlaced } from "./events";
import { describe } from "./menu";

executionUnit({ id: "dispatch" });

const TABLE = "nomnom-assignments" as const;

const assignments = persist(new DynamoDBClient({}), { id: "assignments" });

// The seam. Uncompiled this is the inventory module and `await
// inventory.confirm(...)` is an ordinary call; compiled, the import above is
// removed, inventory's code is not in this bundle, and the same await is a
// request to the deployed inventory function.
const inventory = remote(inventoryModule, { id: "inventory" });

/** Couriers, in the least sophisticated way that is still honest about being a
 *  decision this unit makes. */
const COURIERS = ["ana", "bo", "chidi", "dae"];

function courierFor(orderId: string): string {
  const sum = [...Buffer.from(orderId)].reduce((a, b) => a + b, 0);
  return COURIERS[sum % COURIERS.length];
}

/**
 * Handle an orderPlaced message.
 *
 * `async` because it awaits a remote call, not because anything calls this one
 * over the wire. Nothing does: it is a subscriber.
 */
async function assign(message: OrderPlaced) {
  const orderId = message.order_id ?? "unknown";
  const courier = courierFor(orderId);

  await inventory.confirm(orderId);

  await assignments.send(
    new PutItemCommand({
      TableName: TABLE,
      Item: {
        id: { S: orderId },
        courier: { S: courier },
        summary: { S: describe(message.items ?? []) },
      },
    }),
  );
  await courierAssigned.publish({ order_id: orderId, courier });
  return { order_id: orderId, courier };
}

orderPlaced.subscribe(assign);
