"""The menu, served from a Kubernetes pod.

The third kind of compute in this application, and the one with the tightest
constraints -- which is why it is *this* unit and not another.

A pod gets no AWS identity: IRSA is not emitted yet, so a Kubernetes unit that
reached a store would be warned about at compile time. This one reaches none.
Everything it serves comes out of ``nomnom/menu.py``, which is ordinary shared
code -- the catalogue, the delivery fee, the arithmetic -- and is exactly the
sort of thing that is read constantly, changes rarely, and needs no state.

It is also neither called nor subscribed to, and that is not a coincidence
either. A remote call is an *invocation* and only a function has one; a topic
delivery is *pushed* to a function and nothing polls on a container's behalf.
The compiler refuses both rather than letting them fail at runtime, so the units
that can be containers are exactly the ones that only serve HTTP.
"""

from fastapi import FastAPI

import cloudcompiler as cloudcc

from nomnom.menu import CATALOG, DELIVERY_CENTS, order_total

cloudcc.execution_unit(id="menu")

app = FastAPI()
cloudcc.expose(app, id="nomnom-menu")


@app.get("/health")
async def health():
    return {"status": "ok"}


@app.get("/menu")
async def menu():
    """Everything on sale, and what delivery costs."""
    return {
        "items": [{"sku": sku, "cents": cents} for sku, cents in sorted(CATALOG.items())],
        "delivery_cents": DELIVERY_CENTS,
    }


@app.get("/menu/{sku}")
async def item(sku: str):
    """One item, priced as the catalogue has it.

    The *catalogue* price, which is not always the price charged: pricing owns
    that, holds a table of overrides, and is a Lambda because the storefront
    calls it. This unit is the shop window rather than the till.
    """
    cents = CATALOG.get(sku)
    if cents is None:
        return {"sku": sku, "cents": None, "known": False}
    return {"sku": sku, "cents": cents, "known": True, "with_delivery": order_total([cents])}
