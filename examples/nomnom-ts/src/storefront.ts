// NomNom: the storefront, in TypeScript.
//
// The only unit anything outside can reach. It fronts five others, and the two
// ways it reaches them are the point of this example:
//
//     await pricing.quoteBasket(items)      a call    -- it waits for the answer
//     await orderPlaced.publish({...})      a message -- it does not
//
// Placing an order needs a price and a reservation, and neither is something
// the storefront can guess, so those are calls and a failure in either fails
// the order. Everything that happens afterwards -- finding a courier, notifying
// the customer -- is nobody's business here, so it goes out as one message and
// this unit stops thinking about it.
//
// Uncompiled, the imports below are real modules and every await is an ordinary
// in-process call: `npx tsx src/storefront.ts` runs the whole application as
// one process. Compiled, those imports are gone, the other units' code is not
// in this bundle, and the same awaits are Lambda invocations.

import { randomUUID } from "node:crypto";

import {
  DeleteItemCommand,
  DynamoDBClient,
  GetItemCommand,
  PutItemCommand,
} from "@aws-sdk/client-dynamodb";
import express, { type Request, type RequestHandler, type Response } from "express";
import { executionUnit, expose, persist, remote } from "@cloudcompiler/sdk";

import * as inventoryModule from "@/nomnom/inventory";
import * as pricingModule from "@/nomnom/pricing";
import * as trackingModule from "@/nomnom/tracking";
import { orderPlaced } from "@/nomnom/events";
import type { BasketItem } from "@/nomnom/menu";

executionUnit({ id: "storefront" });

const TABLE = "nomnom-orders" as const;

const app = express();
app.use(express.json());
expose(app, { id: "nomnom-api" });

const orders = persist(new DynamoDBClient({}), { id: "orders" });

// Three seams, three lines. Each names a unit that exists in this program, and
// the compiler checks -- against that unit's source -- that every function
// called below exists and is async. A typo here is a compile error, not a 500
// in whichever request first takes that branch.
const pricing = remote(pricingModule, { id: "pricing" });
const inventory = remote(inventoryModule, { id: "inventory" });
const tracking = remote(trackingModule, { id: "tracking" });

type Handler = (req: Request, res: Response) => Promise<void> | void;
const route =
  (handler: Handler): RequestHandler =>
  (req, res, next) =>
    Promise.resolve(handler(req, res)).catch(next);

/** Price, reserve, record, announce. */
app.post(
  "/orders",
  route(async (req, res) => {
    const order = (req.body ?? {}) as { items?: BasketItem[]; order_id?: string; restaurant?: string };
    const items = order.items ?? [];
    if (items.length === 0) {
      res.status(400).json({ detail: "an order needs items" });
      return;
    }

    const orderId = order.order_id ?? randomUUID().replace(/-/g, "").slice(0, 12);

    const quote = await pricing.quoteBasket(items);
    const reservation = await inventory.reserve(orderId, items);

    await orders.send(
      new PutItemCommand({
        TableName: TABLE,
        Item: {
          id: { S: orderId },
          restaurant: { S: order.restaurant ?? "unknown" },
          total_cents: { N: String(quote.total_cents) },
          state: { S: reservation.state },
        },
      }),
    );

    // A statement, not a question. Dispatch and notify each react on their own
    // schedule, and adding a third listener would not change this line.
    await orderPlaced.publish({
      order_id: orderId,
      items,
      total_cents: quote.total_cents,
    });

    res.json({
      order_id: orderId,
      total_cents: quote.total_cents,
      lines: quote.lines,
      state: reservation.state,
    });
  }),
);

app.get(
  "/orders/:orderId",
  route(async (req, res) => {
    const out = await orders.send(
      new GetItemCommand({ TableName: TABLE, Key: { id: { S: req.params.orderId } } }),
    );
    if (!out.Item) {
      res.status(404).json({ detail: "no such order" });
      return;
    }
    // Where the food is belongs to tracking, and asking is a call because the
    // customer is waiting for the answer.
    const status = await tracking.orderStatus(req.params.orderId);
    res.json({
      order_id: req.params.orderId,
      restaurant: out.Item.restaurant!.S!,
      total_cents: Number(out.Item.total_cents!.N!),
      state: status.state,
      courier: status.courier,
    });
  }),
);

app.delete(
  "/orders/:orderId",
  route(async (req, res) => {
    await inventory.release(req.params.orderId);
    await orders.send(
      new DeleteItemCommand({ TableName: TABLE, Key: { id: { S: req.params.orderId } } }),
    );
    res.json({ ok: true, order_id: req.params.orderId });
  }),
);

app.get(
  "/health",
  route((_req, res) => {
    res.json({ status: "ok" });
  }),
);

export { app };
