"""Unit "api" -- FastAPI on Lambda behind an HTTP API.

FastAPI is the easy web framework for this compiler: an ASGI application is a
single object, `expose` names it, and the route table is discoverable from the
decorators without running anything.

shim: the generated Lambda entry wraps the app with Mangum and exports a
handler. The routes the compiler found become the API Gateway routes, so a
request that FastAPI would not have matched is rejected at the edge rather than
paid for.
"""

from decimal import Decimal

from fastapi import FastAPI, HTTPException

import cloudcompiler as cloudcc

from mega.cloudsdk import presign_upload
from mega.jobs import settle_order
from mega.logs import slog
from mega.messaging import announce
from mega.nosql import cache, documents, orders
from mega.orm import engine
from mega.settings import settings
from mega.storage import recent
from mega.wire import OrderPlaced

cloudcc.execution_unit(id="api")

app = FastAPI()
cloudcc.expose(app, id="storefront")


@app.get("/health")
def health() -> dict:
    return {"status": "ok", "log_level": settings.log_level}


@app.get("/items/{item_id}")
def get_item(item_id: str) -> dict:
    """Three storage capabilities in six lines: cache, document store, SQL."""
    if hit := cache.get(f"item:{item_id}"):
        return {"source": "cache", "item": hit.decode()}

    doc = documents.mega.items.find_one({"_id": item_id})
    if doc is None:
        raise HTTPException(status_code=404, detail="no such item")

    doc["price"] = _price(item_id)
    cache.set(f"item:{item_id}", str(doc), ex=60)
    return {"source": "store", "item": doc}


def _price(item_id: str) -> str:
    with engine.connect() as conn:
        row = conn.exec_driver_sql(
            "SELECT price FROM item WHERE id = %s", (item_id,)
        ).fetchone()
    return str(row[0]) if row else "0.00"


@app.post("/orders")
def place_order(order: OrderPlaced) -> dict:
    """The cross-unit path: write, publish, enqueue.

    Everything that leaves this function goes to another execution unit, and
    every one of them carries a typed payload -- which is what lets the
    compiler check that the worker on the other end agrees.
    """
    orders.put_item(
        Item={
            "order_id": order.order_id,
            "customer_id": order.customer_id,
            "total": Decimal(order.total),
        }
    )
    announce(order)                 # -> unit "worker", via SNS
    settle_order.delay(order)       # -> unit "worker", via Celery
    slog.info("order.placed", order_id=order.order_id)
    return {"ok": True, "receipt_upload": presign_upload(f"{order.order_id}.pdf")}


@app.get("/receipts")
def receipts() -> dict:
    return {"receipts": recent()}
