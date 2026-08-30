"""NomNom: the storefront.

The only unit anything outside can reach. It fronts five others, and the two
ways it reaches them are the point of this example:

    await pricing.quote_basket(items)     a call    -- it waits for the answer
    order_placed.publish({...})           a message -- it does not

Placing an order needs a price and a reservation, and neither is something the
storefront can guess, so those are calls and a failure in either fails the
order. Everything that happens afterwards -- finding a courier, notifying the
customer -- is nobody's business here, so it goes out as one message and this
unit stops thinking about it.

Uncompiled, the imports below are real modules and every await is an ordinary
in-process call: `uvicorn storefront:app` runs the whole application as one
process. Compiled, those imports are gone, the other units' code is not in this
bundle, and the same awaits are Lambda invocations.
"""
# Injected by cloudcc: runtime clients for this program's declared capabilities.
from _cloudcc_runtime import expose as _cloudcc_expose
from _cloudcc_runtime import kv as _cloudcc_kv
from _cloudcc_runtime import rpc as _cloudcc_rpc


import uuid

import boto3
from fastapi import FastAPI, HTTPException



from nomnom.events import order_placed

None

app = FastAPI()
_cloudcc_expose.register(app, id="nomnom-api")

orders = _cloudcc_kv.connect("orders")

# Three seams, three lines. Each names a unit that exists in this program, and
# the compiler checks -- against that unit's source -- that every function
# called below exists and is `async def`. A typo here is a compile error, not a
# 500 in whichever request first takes that branch.
pricing = _cloudcc_rpc.connect("pricing")
inventory = _cloudcc_rpc.connect("inventory")
tracking = _cloudcc_rpc.connect("tracking")


@app.post("/orders")
async def place_order(order: dict):
    """Price, reserve, record, announce."""
    items = order.get("items") or []
    if not items:
        raise HTTPException(status_code=400, detail="an order needs items")

    order_id = order.get("order_id") or uuid.uuid4().hex[:12]

    quote = await pricing.quote_basket(items)
    reservation = await inventory.reserve(order_id, items)

    orders.put_item(
        Item={
            "id": order_id,
            "restaurant": order.get("restaurant", "unknown"),
            "total_cents": quote["total_cents"],
            "state": reservation["state"],
        }
    )

    # A statement, not a question. Dispatch and notify each react on their own
    # schedule, and adding a third listener would not change this line.
    order_placed.publish(
        {
            "order_id": order_id,
            "items": items,
            "total_cents": quote["total_cents"],
        }
    )

    return {
        "order_id": order_id,
        "total_cents": quote["total_cents"],
        "lines": quote["lines"],
        "state": reservation["state"],
    }


@app.get("/orders/{order_id}")
async def get_order(order_id: str):
    item = orders.get_item(Key={"id": order_id}).get("Item")
    if item is None:
        raise HTTPException(status_code=404, detail="no such order")

    # Where the food is belongs to tracking, and asking is a call because the
    # customer is waiting for the answer.
    status = await tracking.order_status(order_id)
    return {
        "order_id": order_id,
        "restaurant": item["restaurant"],
        "total_cents": int(item["total_cents"]),
        "state": status["state"],
        "courier": status["courier"],
    }


@app.delete("/orders/{order_id}")
async def cancel_order(order_id: str):
    await inventory.release(order_id)
    orders.delete_item(Key={"id": order_id})
    return {"ok": True, "order_id": order_id}


@app.get("/health")
async def health():
    return {"status": "ok"}
