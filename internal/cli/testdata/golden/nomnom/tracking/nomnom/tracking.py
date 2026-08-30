"""Tracking: where an order is up to.

The one unit that is reached both ways. It *subscribes* to courierAssigned,
because a courier having been assigned is a statement nobody waits on, and it
is *called* by the storefront, because a customer asking where their food is
wants an answer now.

Both arrive at the same Lambda and the generated entrypoint tells them apart by
the shape of the event. Nothing in this file has to know that.
"""
# Injected by cloudcc: runtime clients for this program's declared capabilities.
from _cloudcc_runtime import kv as _cloudcc_kv


import boto3


from .events import courier_assigned

None

timeline = _cloudcc_kv.connect("trackingEvents")


def on_courier_assigned(message: dict) -> dict:
    """A subscriber, not a remote function: nobody is waiting on the result.

    So it is an ordinary ``def``. The compiler only requires ``async def`` of
    functions something calls over the wire, which this is not -- the message
    arrives on its own.
    """
    order_id = message.get("order_id", "unknown")
    timeline.put_item(
        Item={
            "id": order_id,
            "state": "out-for-delivery",
            "courier": message.get("courier", "unassigned"),
        }
    )
    return {"tracked": order_id}


courier_assigned.subscribe(on_courier_assigned)


async def order_status(order_id: str) -> dict:
    """Called by the storefront while a customer is looking at the page."""
    item = timeline.get_item(Key={"id": order_id}).get("Item")
    if item is None:
        return {"order_id": order_id, "state": "preparing", "courier": None}
    return {
        "order_id": order_id,
        "state": item["state"],
        "courier": item.get("courier"),
    }
