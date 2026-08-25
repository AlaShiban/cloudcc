"""Pub/sub and messaging.

``cloudcc.Topic`` is the portable path, and it is the one capability where the
SDK still supplies the object -- because a topic is not a data store. Nothing
is being kept in it; it is a decision about how messages move, and the decision
is what the compiler needs.

So the topic carries its decisions:

    Topic[T](
        subscribers = "one" | "many",
        ordering    = "none" | "key" | "total",
        delivery    = "at_least_once" | "exactly_once",
        replay      = False | True,
        retention_hours = int,
        max_message_kb  = int,
    )

and **the compiler chooses the backing service from the whole set, or fails**.
That inversion is the point of the category. For a data store the library picks
the capability and `cloudcc.yaml` picks the variant. For a topic there is no
library to ask, and the variants are not interchangeable: SNS cannot replay,
SQS cannot fan out, and FIFO everything costs throughput. Choosing by hand
means knowing all of that; declaring the requirement means the compiler has to.

What it resolves to on AWS today:

===========  ========  ==============  ======  ==========================
subscribers  ordering  delivery        replay  backing
===========  ========  ==============  ======  ==========================
one          none      at_least_once   no      SQS standard
one          key/total exactly_once    no      SQS FIFO
many         none      at_least_once   no      SNS + a queue per subscriber
many         total     exactly_once    no      SNS FIFO + SQS FIFO
any          none/key  any             yes     Kinesis Data Stream
===========  ========  ==============  ======  ==========================

and the combinations that resolve to nothing are errors naming the constraint
to relax, not approximations:

  - ``ordering="total"`` with ``replay=True`` and many subscribers. Total order
    plus replay means one shard, and one shard is a throughput ceiling the
    author has not agreed to. Relax to ``ordering="key"``.
  - ``delivery="exactly_once"`` with ``ordering="none"``. On AWS exactly-once
    is a FIFO property; asking for one without the other describes nothing.
  - ``max_message_kb`` above 256 with an SNS or SQS backing. The message does
    not fit. The fix is a claim check through S3, which changes the failure
    modes enough that it should be asked for rather than inserted.
  - ``retention_hours`` above 336 without ``replay``. SQS holds 14 days;
    anything longer is a stream, and a stream is what ``replay`` asks for.

The alternative -- defaulting to SNS and letting the differences surface in
production -- is the failure this project is organised against. A topic that
silently drops ordering is a bug that reproduces once a week.

**What works today:** the decision layer is implemented -- the requirements are
read, the service is chosen from them, and an impossible set is a compile error
naming the constraint to relax. Of the five services it can choose, only SNS is
provisioned. Selecting one of the other four is a clean error saying which
service was chosen and which requirement forced it:

    "kinesis" is not yet supported for pubsub (declared for "auditEvents").
    cloudcc chose it because replay=True: only a stream lets a subscriber read
    messages sent before it existed.

which is the honest position: choosing correctly and then saying so beats
provisioning something that almost fits.
"""

import aiokafka
import kombu
import pika
from celery import Celery

import cloudcompiler as cloudcc

from .nosql import cache
from .wire import OrderPlaced, RefundRequested, RefundRequestedSchema, ShipmentRequested

# ---------------------------------------------------------------------------
# 1. cloudcc.Topic -- the portable path
# ---------------------------------------------------------------------------
#
# Many subscribers, no ordering requirement, and messages are small. Resolves
# to SNS with a queue per subscriber.
#
# The type parameter is the codec: see mega/wire.py. Both ends of the channel
# read it from the topic's intent in the IR, so a publisher and a subscriber
# cannot disagree about the format.
#
# shim: `_cloudcc_runtime.pubsub.connect(id)` returns a Topic whose `publish`
# encodes with the topic's codec and calls whichever service was chosen, and
# whose `subscribe` is compile-time only -- the subscription becomes an event
# source on the subscribing unit, and the handler is invoked with a decoded
# OrderPlaced.
#
# Uncompiled, `subscribe` registers an in-process callback and `publish` calls
# it synchronously. That is not the same *timing*, and no emulation can make it
# so; what it preserves is that the handler runs, with the right value, and
# that errors surface.
order_events = cloudcc.persist(
    cloudcc.Topic[OrderPlaced](
        subscribers="many",
        ordering="none",
        delivery="at_least_once",
    ),
    id="orderEvents",
)

# One subscriber, and money is involved: a refund applied twice is a refund
# twice. Resolves to SQS FIFO, and the compiler enforces that only one unit
# subscribes -- a second subscriber is a compile error rather than a silent
# split of the messages between them.
refund_events = cloudcc.persist(
    cloudcc.Topic[RefundRequested](
        subscribers="one",
        ordering="key",
        delivery="exactly_once",
        codec=cloudcc.Marshmallow(RefundRequestedSchema()),
    ),
    id="refundEvents",
)

# Replay, because a new consumer needs the history to rebuild its projection.
# Resolves to a Kinesis stream: ordering per key, seven days of retention, and
# a cursor each consumer owns.
#
# Note what changes for the author here, and what does not. `publish` is the
# same call. What differs is that this topic can be read from the beginning,
# which is a property they asked for by name.
audit_events = cloudcc.persist(
    cloudcc.Topic[OrderPlaced](
        subscribers="many",
        ordering="key",
        replay=True,
        retention_hours=168,
    ),
    id="auditEvents",
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
# Kafka is the clearest case of a library declaring the service rather than the
# capability: partitions, offsets and consumer groups have no SNS equivalent.
# It is also the answer for someone who wants the `Topic` above but does not
# want cloudcc choosing -- naming the library is how you take the decision back.
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
    audit_events.publish(event)


async def request_shipment(event: ShipmentRequested) -> None:
    """Publish through Kafka.

    Async because aiokafka is, not because cloudcc made it so: `persist`
    returned the producer synchronously and every await here is one the
    uncompiled program also performs.
    """
    await shipment_producer.send_and_wait("shipments", event)
