# nomnom

A food-delivery application in six execution units, written to show the two
ways units talk to each other and what the compiler does with each.

```
                 ┌──────────────┐
   HTTP ────────▶│  storefront  │
                 └──────┬───────┘
       calls ───────────┼─────────── publishes
        │               │                 │
   ┌────▼────┐   ┌──────▼────┐      ┌─────▼──────┐
   │ pricing │   │ inventory │      │ orderPlaced│
   └─────────┘   └─────▲─────┘      └──┬──────┬──┘
                       │  calls        │      │
                 ┌─────┴────┐◀─────────┘      │
                 │ dispatch │                 │
                 └────┬─────┘                 │
                      │ publishes             │
              ┌───────▼────────┐        ┌─────▼────┐
              │ courierAssigned├───────▶│  notify  │
              └───────┬────────┘        └──────────┘
                      │
                 ┌────▼─────┐
                 │ tracking │◀──── calls ──── storefront
                 └──────────┘
```

`nomnom-architecture.png`, written by every compile, is the same picture drawn
from the compiler's own edges.

## The two ways units talk

**A call is a question.** The caller waits, and if the callee fails the caller
fails:

```python
from nomnom import pricing

pricing = cloudcc.remote(pricing, id="pricing")

quote = await pricing.quote_basket(items)
```

Uncompiled that import is a module and the await is an ordinary in-process
call, so `uvicorn storefront:app` runs the whole application as one process.
Compiled, the import is gone, pricing's code is not in the storefront's bundle,
and the same await is a Lambda invocation.

Three things stop being true once a call crosses a process boundary, and the
compiler checks all three rather than letting them surface in production:

* the function must be **`async def`** — a remote call is a network round trip,
  and a synchronous signature is the one thing that cannot be corrected later,
  because by then every caller has been written to block on it;
* the function must **exist** — `pricing.quote_baskets(...)` is a compile error
  listing what pricing does offer, not an `AttributeError` in whichever request
  first takes that branch;
* the calls must not form a **cycle** — two units awaiting each other deadlock
  until both time out, holding a concurrency slot each.

**A message is a statement.** The publisher does not wait and does not learn
who listened:

```python
order_placed.publish({"order_id": order_id, "items": items})
```

An order having been placed is true whether or not anything reacts to it yet,
and adding a fourth listener is a change to that listener alone.

## The boundary is real

Declaring a unit remote cuts the caller's import closure at it. The storefront
bundles neither pricing's code nor its dependencies, and — because permissions
and environment are both derived from what a unit bundles — it is granted
nothing on pricing's table. Moving the seam, merging two units or splitting one
in three, is a change to the `remote` line rather than to any call site.

## Running it

As one process, with no compiler involved:

```sh
uv run --with fastapi --with uvicorn --with boto3 \
  python -m uvicorn storefront:app --port 8000
```

Compiled and deployed to an emulator, with all six units running separately and
every assertion checked end to end:

```sh
./tests/e2e/nomnom.sh
```

That harness deploys all six units, invokes one directly with a call envelope,
drives the storefront over HTTP, and follows a single order through the whole
chain — including one deployed unit invoking another — to the state it leaves
in five DynamoDB tables and an S3 bucket.
