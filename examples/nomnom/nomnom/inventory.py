"""Inventory: holding stock while an order is being placed.

Called by two units for different reasons -- the storefront reserves during
checkout, dispatch confirms once a courier is on the way -- which is what makes
it worth being a unit rather than a module. Both callers wait for the answer,
because "did the reservation succeed" is not something either can carry on
without.
"""

import json

import boto3

import cloudcompiler as cloudcc

from .menu import describe

cloudcc.execution_unit(id="inventory")

reservations = cloudcc.persist(
    boto3.resource("dynamodb").Table("nomnom-reservations"), id="reservations"
)


async def reserve(order_id: str, items: list[dict]) -> dict:
    """Hold stock for an order, and say whether it worked."""
    reservations.put_item(
        Item={
            "id": order_id,
            "state": "held",
            "items": json.dumps(items),
            "summary": describe(items),
        }
    )
    return {"order_id": order_id, "state": "held"}


async def confirm(order_id: str) -> dict:
    """Turn a hold into a commitment. Called by dispatch."""
    reservations.update_item(
        Key={"id": order_id},
        UpdateExpression="SET #s = :s",
        ExpressionAttributeNames={"#s": "state"},
        ExpressionAttributeValues={":s": "committed"},
    )
    return {"order_id": order_id, "state": "committed"}


async def release(order_id: str) -> dict:
    """Give the stock back."""
    reservations.delete_item(Key={"id": order_id})
    return {"order_id": order_id, "state": "released"}


async def state_of(order_id: str) -> dict:
    item = reservations.get_item(Key={"id": order_id}).get("Item")
    return {"order_id": order_id, "state": item["state"] if item else "none"}
