# NomNom, in TypeScript

Seven units, **three kinds of compute**, three remote calls and two fan-out
topics. The mirror of
`examples/nomnom`, and the example that shows the two ways units reach each
other:

```ts
await pricing.quoteBasket(items)     // a call    -- it waits for the answer
await orderPlaced.publish({ … })     // a message -- it does not
```

Placing an order needs a price and a reservation, and neither is something the
storefront can guess, so those are calls and a failure in either fails the order.
Everything afterwards — finding a courier, notifying the customer — is nobody's
business there, so it goes out as one message.

| unit | compute | reached by | holds |
|---|---|---|---|
| `storefront` | **ECS Fargate** | HTTP | orders (DynamoDB) |
| `menu` | **Kubernetes** | HTTP | nothing |
| `pricing` | Lambda | a call | its own prices table |
| `inventory` | Lambda | calls, from two units | reservations |
| `dispatch` | Lambda | a message | assignments; calls inventory, publishes a message |
| `tracking` | Lambda | **both** a message and a call | the timeline |
| `notify` | Lambda | messages, from both topics | an S3 outbox |

## Three kinds of compute, and why these ones

The split is not a free choice, and the compiler is what decides it:

* **A remote call is an invocation, and only a function has one.** A container
  is reached at an address by something that already knows the address; there is
  no API that says "run this and give me the answer". `remote()` pointed at a
  container is a compile error.
* **A topic delivery is pushed to a function**, and nothing polls on a
  container's behalf. A container that subscribes is a compile error too.

So the units that *can* be containers are exactly the ones that only serve HTTP:
`storefront` and `menu`. Everything called or subscribed to is a function.
Between them the two containers show both platforms — Fargate and Kubernetes —
which is the second portable axis: dropping `platform: kubernetes` from `menu`
moves it to Fargate and changes nothing else.

`menu` reaches no store on purpose. A pod's AWS identity comes from IRSA, which
`cloudcc` does not emit yet, so a Kubernetes unit that reached one would be
warned about at compile time — and the menu genuinely needs nothing but the
catalogue it is compiled with.

## What TypeScript adds to it

**The topics carry their message types.** `Topic<OrderPlaced>` and
`Topic<CourierAssigned>` mean a publisher and every subscriber are checked
against one another. In the Python original that agreement is a convention; here
it is a compile error when it breaks.

**The remote boundary is visible in the types too.** `remote(pricingModule, …)`
returns the module's own type, so `await pricing.quoteBasket(items)` is checked
against `pricing.ts` — the compiler already refuses a function that does not
exist or is not `async`, and the editor now refuses the wrong argument as well.

**And the boundary really cuts.** Compiled, `storefront`'s bundle contains
`storefront.ts` and `nomnom/events.ts` and nothing else: pricing, inventory and
tracking are not in it, which is why the storefront's IAM policy does not
mention their tables. `tests/e2e/nomnom.sh nomnom-ts` checks that by looking.

## Running it

Uncompiled it is one process and every await is an ordinary in-process call:

```bash
npx tsx src/storefront.ts
```

That is also why this example is not in the differential suite: uncompiled, a
published message reaches only the subscribers this process happens to have
imported — which is none of them. Compiled, the same publish fans out to two
units that wake, call a third, and write to four stores. Both behaviours are
correct; they are not the same, and that difference is the feature.

```bash
./tests/e2e/nomnom.sh nomnom-ts     # deploys all six and follows the chain
```
