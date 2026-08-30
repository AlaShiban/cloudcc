"""Notifications: the outbox.

Subscribes to both topics and writes a record for each. It calls nothing and
nothing calls it, which is what a notification service should look like: adding
it changed no other unit, and removing it would change none either.

Its store is a bucket rather than a table, declared by wrapping a
``pathlib.Path``. Uncompiled that is a directory on disk and the writes are
files; compiled it is S3 and the writes are objects. The code is the same
either way, which is the point of persisting a client rather than an API of
ours.
"""
# Injected by cloudcc: runtime clients for this program's declared capabilities.
from _cloudcc_runtime import fs as _cloudcc_fs


import json
import pathlib


from .events import courier_assigned, order_placed
from .menu import describe

None

outbox = _cloudcc_fs.connect("notifications")


def _write(name: str, payload: dict) -> None:
    """Write one record.

    ``/`` then ``write_bytes`` -- the pathlib spelling, which is what the
    compiled client answers to as well, because it is a cloudpathlib S3Path
    rather than an API of cloudcc's.
    """
    outbox.mkdir(parents=True, exist_ok=True)
    (outbox / name).write_bytes(json.dumps(payload, sort_keys=True).encode("utf-8"))


def on_order_placed(message: dict) -> dict:
    order_id = message.get("order_id", "unknown")
    _write(
        f"{order_id}-placed.json",
        {
            "kind": "order_placed",
            "order_id": order_id,
            "basket": describe(message.get("items", [])),
            "total_cents": message.get("total_cents"),
        },
    )
    return {"notified": order_id}


def on_courier_assigned(message: dict) -> dict:
    order_id = message.get("order_id", "unknown")
    _write(
        f"{order_id}-courier.json",
        {
            "kind": "courier_assigned",
            "order_id": order_id,
            "courier": message.get("courier"),
        },
    )
    return {"notified": order_id}


order_placed.subscribe(on_order_placed)
courier_assigned.subscribe(on_courier_assigned)
