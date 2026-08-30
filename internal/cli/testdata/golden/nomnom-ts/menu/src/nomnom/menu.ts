// The menu, and the arithmetic over it.
//
// Nothing here is a capability. It is ordinary shared code, imported by several
// units, and it is in the example on purpose: most of a program is this, and
// the units that need it each get a copy in their bundle without anybody saying
// so.

/** One line of a basket, as a customer sends it. */
export interface BasketItem {
  sku: string;
  qty?: number;
}

/** Fallback prices in cents, used when the pricing table has no row for a sku. */
export const CATALOG: Record<string, number> = {
  margherita: 1200,
  pepperoni: 1450,
  "garlic-bread": 500,
  cola: 300,
};

/** Delivery is flat, which is a business decision rather than a technical one. */
export const DELIVERY_CENTS = 349;

export function lineTotal(unitCents: number, qty: number): number {
  return unitCents * Math.trunc(qty);
}

export function orderTotal(lineCents: number[]): number {
  return lineCents.reduce((a, b) => a + b, 0) + DELIVERY_CENTS;
}

/** A one-line summary of a basket, for notifications and audit records. */
export function describe(items: BasketItem[]): string {
  return items.map((item) => `${item.qty ?? 1}x ${item.sku ?? "?"}`).join(", ");
}
