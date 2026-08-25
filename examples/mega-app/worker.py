"""Unit "worker" -- a container that serves nothing and consumes everything.

This unit exposes no application, so there is no ALB, no API Gateway and no
handler. What makes it a unit is that things are addressed to it: a topic
subscription, a Celery queue, an RQ queue, a dramatiq broker and a Kafka
consumer group.

That is the case worth designing carefully, because the author never wrote
"scale this on queue depth" anywhere. The compiler knows the subscriptions, so
it is the only party that can:

  - decide the worker is a container rather than a Lambda when it holds a
    long-lived consumer loop, and a Lambda when every entry point is a
    short-lived handler -- these five consumers disagree, and Kafka is the one
    that forces a container;
  - scale on the depth of the queues that feed it, not on CPU;
  - give the process a shutdown that drains rather than one that drops.
"""

import asyncio

import aiokafka
import msgspec
from tortoise import Tortoise

import cloudcompiler as cloudcc

from mega.jobs import settle_order  # noqa: F401 -- the worker must ship the task
from mega.logs import log
from mega.messaging import audit_connection, order_events, rabbit_params, refund_events
from mega.nosql import analytics, metrics_cluster
from mega.orm import reporting_db, tortoise_config
from mega.storage import store
from mega.wire import OrderPlaced, RefundRequested, ShipmentRequested

cloudcc.execution_unit(id="worker")


# ---------------------------------------------------------------------------
# Subscriptions: the compiler's view of what this unit is for
# ---------------------------------------------------------------------------
#
# The annotation is load-bearing. `order_events` is a `Topic[OrderPlaced]`, so
# a handler annotated with anything else is a compile error rather than a
# poison message -- and this is the bug that no unit test catches, because the
# publisher and the subscriber are never in the same test.
def on_order(event: OrderPlaced) -> None:
    log.info("settling %s", event.order_id)
    store(event.order_id, f"receipt for {event.order_id}".encode())
    reporting_db.execute_sql(
        "INSERT INTO daily_sales (order_id, total) VALUES (%s, %s)",
        (event.order_id, str(event.total)),
    )


def on_refund(event: RefundRequested) -> None:
    """Decoded by the marshmallow schema named on the topic, not by this
    signature -- which is exactly why the codec has to live on the channel."""
    analytics.execute(
        "INSERT INTO refunds (order_id, reason, amount) VALUES", [(event.order_id, event.reason, event.amount)]
    )


order_events.subscribe(on_order)
refund_events.subscribe(on_refund)


# ---------------------------------------------------------------------------
# A Kafka consumer loop: the reason this unit is a container
# ---------------------------------------------------------------------------
#
# There is no way to express a consumer group as a Lambda event source without
# giving up the offset semantics that were the reason to choose Kafka. So a
# program containing this loop pins its unit to `type: ecs`, and a cloudcc.yaml
# that says `lambda` should be a clean error naming this line -- not a
# deployment that starts and then quietly processes nothing.
#
# msgspec decodes straight into the struct, with the type passed in. No
# registry, no import of the producer beyond the type itself.
async def consume_shipments() -> None:
    consumer = aiokafka.AIOKafkaConsumer(
        "shipments", bootstrap_servers="localhost:9092", group_id="mega-worker"
    )
    await consumer.start()
    try:
        async for message in consumer:
            event = msgspec.json.decode(message.value, type=ShipmentRequested)
            log.info("shipment for %s to %s", event.order_id, event.warehouse)
    finally:
        await consumer.stop()


# ---------------------------------------------------------------------------
# The other three brokers, used as their libraries intend
# ---------------------------------------------------------------------------
def consume_audit() -> None:
    """kombu: a lazy Connection, connected where the program chooses to."""
    with audit_connection as conn:
        queue = conn.SimpleQueue("audit")
        while True:
            message = queue.get(block=True)
            log.info("audit %s", message.payload)
            message.ack()


def rabbit_channel():
    """pika: the parameters were persisted, the connection is the program's."""
    import pika

    return pika.BlockingConnection(rabbit_params).channel()


async def main() -> None:
    """The container's entrypoint.

    `Tortoise.init` is awaited here, by the application, not by `persist` --
    which is how an async-only ORM fits a synchronous `persist` without making
    every binding a coroutine.
    """
    await Tortoise.init(config=tortoise_config)
    session = metrics_cluster.connect("mega")
    session.execute("USE mega")
    await consume_shipments()


if __name__ == "__main__":
    asyncio.run(main())
