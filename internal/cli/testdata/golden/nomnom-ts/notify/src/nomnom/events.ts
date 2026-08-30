// The two topics, declared once and imported by everyone who uses them.
//
// This is the other half of how units talk to each other, and the difference
// from `remote` is the whole point of having both:
//
// * A **call** is a question. The caller waits, the answer comes back, and if
//   the other unit is down the caller fails.
// * A **message** is a statement. The publisher does not wait and does not
//   learn who listened.
//
// Both topics fan out, so both resolve to SNS. Nothing here names SNS: the
// requirements say many subscribers, no ordering guarantee, at-least-once, and
// the compiler chooses from that.
//
// Each topic carries its own message type, which is what TypeScript adds to the
// arrangement: a publisher and every subscriber are checked against one
// another, so a field renamed on one side is a compile error rather than an
// `undefined` in a store nobody reads.


// Injected by cloudcc: runtime clients for this program's declared capabilities.
import * as _cloudccPubsub from "../../_cloudcc_runtime/pubsub.js";

import type { BasketItem } from "./menu";

export interface OrderPlaced {
  order_id: string;
  items: BasketItem[];
  total_cents: number;
}

export interface CourierAssigned {
  order_id: string;
  courier: string;
}

/** Fan-out, unordered, at-least-once: the defaults, spelled out. */
export const orderPlaced = _cloudccPubsub.connect("orderPlaced");

/** Same shape. A separate topic rather than a "type" field on the first,
 *  because subscribers differ. */
export const courierAssigned = _cloudccPubsub.connect("courierAssigned");
