"""Dispatch: finding a courier once an order has been placed.

The unit that does both things in one direction. It is woken by a *message*
(orderPlaced), and while handling it makes a *call* (inventory.confirm) and
publishes another message (courierAssigned).

That mix is deliberate. Confirming the reservation is a question -- if it fails
the order should not be dispatched -- so it is a call and dispatch waits.
Telling everyone a courier is on the way is a statement, so it is a message and
dispatch does not.
"""
# Injected by cloudcc: runtime clients for this program's declared capabilities.
from _cloudcc_runtime import kv as _cloudcc_kv
from _cloudcc_runtime import rpc as _cloudcc_rpc


import boto3



from .events import courier_assigned, order_placed
from .menu import describe

None

assignments = _cloudcc_kv.connect("assignments")

# The seam. Uncompiled this is the inventory module and `await
# inventory.confirm(...)` is an ordinary call; compiled, the import above is
# removed, inventory's code is not in this bundle, and the same await is a
# request to the deployed inventory function.
inventory = _cloudcc_rpc.connect("inventory")

#: Couriers, in the least sophisticated way that is still honest about being a
#: decision this unit makes.
COURIERS = ["ana", "bo", "chidi", "dae"]


def _courier_for(order_id: str) -> str:
    return COURIERS[sum(order_id.encode()) % len(COURIERS)]


async def assign(message: dict) -> dict:
    """Handle an orderPlaced message.

    ``async def`` because it awaits a remote call, not because anything calls
    this one over the wire. Nothing does: it is a subscriber.
    """
    order_id = message.get("order_id", "unknown")
    courier = _courier_for(order_id)

    await inventory.confirm(order_id)

    assignments.put_item(
        Item={
            "id": order_id,
            "courier": courier,
            "summary": describe(message.get("items", [])),
        }
    )
    courier_assigned.publish({"order_id": order_id, "courier": courier})
    return {"order_id": order_id, "courier": courier}


order_placed.subscribe(assign)
