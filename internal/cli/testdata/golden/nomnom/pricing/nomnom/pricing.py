"""Pricing: what a basket costs.

An execution unit that nothing exposes to the network. It exists because other
units call it, and the only way in is ``cloudcc.remote``.

Everything public here is ``async def``, which the compiler requires of
anything reached over the wire. That is not a style rule: compiled, each of
these is a network round trip, and a synchronous signature is the one thing
that cannot be fixed afterwards -- by then every caller has been written to
block on it.
"""
# Injected by cloudcc: runtime clients for this program's declared capabilities.
from _cloudcc_runtime import kv as _cloudcc_kv


import boto3


from .menu import CATALOG, line_total, order_total

None

#: This unit's own table, and nobody else's. A caller reaching pricing over the
#: wire is not given access to it -- the remote boundary cuts the import
#: closure, so no other unit's IAM policy mentions this table.
prices = _cloudcc_kv.connect("menuPrices")


def _unit_price(sku: str) -> int:
    """The current price of one sku, in cents.

    Private, so it is not part of what this unit offers over the wire: the
    compiler will not let another unit call it, and a rename here is not a
    breaking change to anyone.
    """
    item = prices.get_item(Key={"id": sku}).get("Item")
    if item is not None:
        return int(item["cents"])
    return CATALOG.get(sku, 0)


async def quote_basket(items: list[dict]) -> dict:
    """Price a basket. Called by the storefront on every order."""
    lines = []
    for item in items:
        unit = _unit_price(item["sku"])
        lines.append(
            {
                "sku": item["sku"],
                "qty": int(item.get("qty", 1)),
                "unit_cents": unit,
                "line_cents": line_total(unit, item.get("qty", 1)),
            }
        )
    return {
        "lines": lines,
        "total_cents": order_total([line["line_cents"] for line in lines]),
    }


async def set_price(sku: str, cents: int) -> dict:
    """Change a price.

    Here so the example has a write path into this unit's own store that goes
    through a remote call, which is what the integration test uses to prove a
    call really reached the deployed function and really persisted.
    """
    prices.put_item(Item={"id": sku, "cents": int(cents)})
    return {"sku": sku, "cents": int(cents)}
