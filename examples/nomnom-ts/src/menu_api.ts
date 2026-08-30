// The menu, served from a Kubernetes pod.
//
// The third kind of compute in this application, and the one with the tightest
// constraints -- which is why it is *this* unit and not another.
//
// A pod gets no AWS identity: IRSA is not emitted yet, so a Kubernetes unit
// that reached a store would be warned about at compile time. This one reaches
// none. Everything it serves comes out of `nomnom/menu.ts`, which is ordinary
// shared code -- the catalogue, the delivery fee, the arithmetic -- and is
// exactly the sort of thing that is read constantly, changes rarely, and needs
// no state.
//
// It is also neither called nor subscribed to, and that is not a coincidence
// either. A remote call is an *invocation* and only a function has one; a topic
// delivery is *pushed* to a function and nothing polls on a container's behalf.
// The compiler refuses both rather than letting them fail at runtime, so the
// units that can be containers are exactly the ones that only serve HTTP.

import express, { type Request, type Response } from "express";
import { executionUnit, expose } from "@cloudcompiler/sdk";

import { CATALOG, DELIVERY_CENTS, orderTotal } from "@/nomnom/menu";

executionUnit({ id: "menu" });

const app = express();
expose(app, { id: "nomnom-menu" });

app.get("/health", (_req: Request, res: Response) => {
  res.json({ status: "ok" });
});

/** Everything on sale, and what delivery costs. */
app.get("/menu", (_req: Request, res: Response) => {
  res.json({
    items: Object.keys(CATALOG)
      .sort()
      .map((sku) => ({ sku, cents: CATALOG[sku] })),
    delivery_cents: DELIVERY_CENTS,
  });
});

/**
 * One item, priced as the catalogue has it.
 *
 * The *catalogue* price, which is not always the price charged: pricing owns
 * that, holds a table of overrides, and is a Lambda because the storefront
 * calls it. This unit is the shop window rather than the till.
 */
app.get("/menu/:sku", (req: Request, res: Response) => {
  const cents = CATALOG[req.params.sku];
  if (cents === undefined) {
    res.json({ sku: req.params.sku, cents: null, known: false });
    return;
  }
  res.json({ sku: req.params.sku, cents, known: true, with_delivery: orderTotal([cents]) });
});

// Exported, not listened to: the generated container entry starts the server.
export { app };
