"""Pub/sub and messaging.

``cloudcc.Topic`` is the portable path: no broker to choose, no client library,
and the compiler is free to pick SNS, or EventBridge, or SQS fan-out. Everything
else in this file is the "I already have a broker and I know which one" path,
and there the library *is* the choice -- a program written against Kafka's
ordering guarantees is not a program that can run on SNS.

So the rule for this category is the inverse of the storage one:

    For storage, the library declares a capability and ``cloudcc.yaml`` picks
    the variant. For messaging, the library often declares the *service*, and
    the compiler's job is to provision that service and wire it up, not to
    offer alternatives.
"""

import aiokafka
import kombu
import pika
from celery import Celery

import cloudcompiler as cloudcc

from .nosql import cache
from .wire import OrderPlaced, RefundRequested, RefundRequestedSchema, ShipmentRequested

# ---------------------------------------------------------------------------
# 1. cloudcc.Topic -- supported today, typed here (proposed)
# ---------------------------------------------------------------------------
#
# The type parameter is the codec: see mega/wire.py. Both ends of the channel
# read it from the topic's intent in the IR, so the publisher and the
# subscriber cannot disagree about the format.
#
# shim: `_cloudcc_runtime.pubsub.connect(id)` returns a Topic whose `publish`
# encodes with the topic's codec and calls SNS, and whose `subscribe` is
# compile-time only -- the subscription becomes an event-source mapping on the
# subscribing unit, and the handler is invoked with a decoded OrderPlaced.
#
# Uncompiled, `subscribe` registers an in-process callback and `publish` calls
# it synchronously, so a single-process run behaves the same way. It is not the
# same *timing*, and no emulation can make it so; what it preserves is that the
# handler runs, with the right value, and errors surface.
order_events = cloudcc.persist(cloudcc.Topic[OrderPlaced](), id="orderEvents")

# The same, with an explicit codec, because marshmallow's schema is a separate
# object and the type alone would not name it.
refund_events = cloudcc.persist(
    cloudcc.Topic[RefundRequested](codec=cloudcc.Marshmallow(RefundRequestedSchema())),
    id="refundEvents",
)


# ---------------------------------------------------------------------------
# 2. Celery -- proposed, and the one client that declares two resources
# ---------------------------------------------------------------------------
#
# A Celery app names a broker *and* a result backend. Those are two different
# resources with different lifetimes -- the broker is a queue, the backend is a
# store -- and one wrapped object declares both.
#
# That is worth stating explicitly because every other entry in the client
# table is one-to-one. The intent layer already supports it (a hint may produce
# several intents); what it needs is a way to give each one an id. Hence the
# two-id form below, which reads better than persisting the same object twice.
#
# shim: returns a `Celery` app whose broker URL points at the provisioned queue
# and whose backend points at the provisioned store, credentials resolved from
# managed secrets. Celery does not connect until the first `delay()` or until a
# worker starts, so nothing happens at import.
#
# open question: `broker_url` is read from the app's conf at first use, and
# Celery supports a callable through `broker_transport_options` only partially.
# Confirm the password can be late-bound; if it cannot, the broker credential
# has to be materialised into the environment at unit init, which is a weaker
# position than the rest of this file but not an unusual one.
celery_app = cloudcc.persist(
    Celery("mega", broker="redis://localhost:6379/0", backend="redis://localhost:6379/1"),
    id={"broker": "taskBroker", "backend": "taskResults"},
)


# ---------------------------------------------------------------------------
# 3. Pika (RabbitMQ / AMQP) -- proposed
# ---------------------------------------------------------------------------
#
# `pika.BlockingConnection` opens a socket. `pika.ConnectionParameters` does
# not, and the application passes it to whichever connection type it wants --
# blocking, select, asyncio. So the parameters object is what gets wrapped, and
# the program keeps control of its own connection lifetime.
#
# shim: returns ConnectionParameters pointed at the provisioned Amazon MQ for
# RabbitMQ broker, with TLS and `PlainCredentials` from the managed secret.
#
# open question: Amazon MQ brokers take minutes to provision and are not
# free-tier friendly, so the local emulation and the deployed resource differ
# in cost by more than anything else in this example. Worth a compile-time
# note the first time one appears in a program.
rabbit_params = cloudcc.persist(
    pika.ConnectionParameters(host="localhost"),
    id="workQueue",
)


# ---------------------------------------------------------------------------
# 4. aiokafka -- proposed
# ---------------------------------------------------------------------------
#
# `AIOKafkaProducer(...)` constructs without connecting; the application awaits
# `start()`. Same shape as asyncpg, and the same clean fit.
#
# shim: returns a producer whose `bootstrap_servers` are the provisioned MSK
# cluster's brokers, with `security_protocol="SASL_SSL"` and the IAM token
# provider installed.
#
# Kafka is the clearest case of the library declaring the service: partitions,
# offsets and consumer groups have no SNS equivalent, so there is nothing for
# `cloudcc.yaml` to choose between beyond MSK and MSK Serverless.
shipment_producer = cloudcc.persist(
    aiokafka.AIOKafkaProducer(bootstrap_servers="localhost:9092"),
    id="shipmentStream",
)


# ---------------------------------------------------------------------------
# 5. kombu -- proposed
# ---------------------------------------------------------------------------
#
# `kombu.Connection` is lazy -- it is a URL and a set of options until someone
# calls `connect()` or uses it as a context manager -- so it is exactly the
# cheapest object that names the resource.
#
# kombu speaks to several transports, so the URL scheme picks the service the
# same way a SQLAlchemy URL picks the engine: `amqp://` asks for Amazon MQ,
# `sqs://` for SQS, `redis://` for the persisted Redis.
#
# shim: returns a Connection with the resolved URL and credentials.
audit_connection = cloudcc.persist(
    kombu.Connection("amqp://guest:guest@localhost//"),
    id="auditQueue",
)


# ---------------------------------------------------------------------------
# 6. Redis pub/sub -- supported today, and a detail worth catching
# ---------------------------------------------------------------------------
#
# Nothing new is declared: this is the client from mega/nosql.py. But the
# compiler should notice the `.pubsub()` call, because it changes what the
# resource has to be:
#
#   - a cache can be evicted under memory pressure; a channel a subscriber is
#     blocked on cannot;
#   - `notify-keyspace-events` and pub/sub both need parameter-group settings
#     that the default cache configuration does not have;
#   - MemoryDB and ElastiCache differ here in ways that matter.
#
# So `persist_redis` with a `.pubsub()` call in the program is a different
# intent from `persist_redis` without one, and the difference should be visible
# in the plan rather than discovered when messages start disappearing.
channel = cache.pubsub()
channel.subscribe("order-events")


def announce(event: OrderPlaced) -> None:
    """Publish through the portable path."""
    order_events.publish(event)


async def request_shipment(event: ShipmentRequested) -> None:
    """Publish through Kafka.

    Async because aiokafka is, not because cloudcc made it so: `persist`
    returned the producer synchronously and every await here is one the
    uncompiled program also performs.
    """
    await shipment_producer.send_and_wait("shipments", event)
