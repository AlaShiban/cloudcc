// Pricing: what a basket costs.
//
// An execution unit that nothing exposes to the network. It exists because
// other units call it, and the only way in is `remote`.
//
// Everything exported here is `async`, which the compiler requires of anything
// reached over the wire. That is not a style rule: compiled, each of these is a
// network round trip, and a synchronous signature is the one thing that cannot
// be fixed afterwards -- by then every caller has been written to block on it.

// Injected by cloudcc: runtime clients for this program's declared capabilities.
import * as _cloudccKv from "../../_cloudcc_runtime/kv.js";

import { DynamoDBClient, GetItemCommand, PutItemCommand } from "@aws-sdk/client-dynamodb";

import { CATALOG, lineTotal, orderTotal, type BasketItem } from "./menu";

undefined;

const TABLE = "nomnom-prices" as const;

// This unit's own table, and nobody else's. A caller reaching pricing over the
// wire is not given access to it -- the remote boundary cuts the import
// closure, so no other unit's IAM policy mentions this table.
const prices = _cloudccKv.connect("menuPrices") as DynamoDBClient;

export interface QuoteLine {
  sku: string;
  qty: number;
  unit_cents: number;
  line_cents: number;
}

/**
 * The current price of one sku, in cents.
 *
 * Not exported, so it is not part of what this unit offers over the wire: the
 * compiler will not let another unit call it, and a rename here is not a
 * breaking change to anyone.
 */
async function unitPrice(sku: string): Promise<number> {
  const out = await prices.send(
    new GetItemCommand({ TableName: TABLE, Key: { id: { S: sku } } }),
  );
  if (out.Item) {
    return Number(out.Item.cents!.N!);
  }
  return CATALOG[sku] ?? 0;
}

/** Price a basket. Called by the storefront on every order. */
export async function quoteBasket(
  items: BasketItem[],
): Promise<{ lines: QuoteLine[]; total_cents: number }> {
  const lines: QuoteLine[] = [];
  for (const item of items) {
    const unit = await unitPrice(item.sku);
    const qty = Math.trunc(item.qty ?? 1);
    lines.push({ sku: item.sku, qty, unit_cents: unit, line_cents: lineTotal(unit, qty) });
  }
  return { lines, total_cents: orderTotal(lines.map((l) => l.line_cents)) };
}

/**
 * Change a price.
 *
 * Here so the example has a write path into this unit's own store that goes
 * through a remote call, which is what the integration test uses to prove a
 * call really reached the deployed function and really persisted.
 */
export async function setPrice(sku: string, cents: number): Promise<{ sku: string; cents: number }> {
  await prices.send(
    new PutItemCommand({
      TableName: TABLE,
      Item: { id: { S: sku }, cents: { N: String(Math.trunc(cents)) } },
    }),
  );
  return { sku, cents: Math.trunc(cents) };
}
