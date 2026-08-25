"""Task queues -- work that has to be executed, as distinct from messages.

Pub/sub says "this happened, whoever cares may listen". A task queue says "run
this, and tell me when it is done". The difference is not stylistic: it changes
what the compiler has to produce.

A task function is a **callable boundary between execution units**. Its
arguments are serialised, sent, and reconstituted in another process -- which
means everything mega/wire.py says about codecs applies here, plus one thing it
does not have to worry about: a task has a return value, and someone may be
waiting for it.

So for each declared task the compiler should:

  - derive a worker execution unit from the functions themselves, rather than
    make the author write one by hand and keep it in sync;
  - type-check the call site against the task's signature. ``add.delay(1, "2")``
    is a bug that today surfaces as a traceback in a worker log an hour later,
    and it is a compile error here;
  - choose the codec for the arguments the same way a topic does, and refuse
    an argument type that cannot cross the boundary. A task that takes a
    database session is not a task;
  - scale the worker on queue depth, because that is the only signal that
    means anything for this workload.
"""

import dramatiq
from dramatiq.brokers.redis import RedisBroker
from rq import Queue

import cloudcompiler as cloudcc

from .messaging import celery_app
from .nosql import cache
from .wire import OrderPlaced

# ---------------------------------------------------------------------------
# 1. Celery -- proposed
# ---------------------------------------------------------------------------
#
# The app is declared in mega/messaging.py, since that is where its broker and
# result backend are. Here it is used.
#
# shim: none beyond the app itself. The compiler:
#   - collects every `@celery_app.task` in the program and gives them a worker
#     unit whose command is `celery -A ... worker`, on ECS, scaled on the
#     queue's depth;
#   - grants the publishing units send permission and the worker unit receive
#     permission on the broker, and read/write on the result backend;
#   - records each task's signature on the intent so a call site can be
#     checked against it.
#
# Uncompiled, `task_always_eager` makes `.delay()` run inline. That is the same
# bargain as an in-process topic: the code path is exercised and the timing is
# not.


@celery_app.task(name="mega.settle_order")
def settle_order(event: OrderPlaced) -> str:
    """Settle payment for an order.

    The annotation is what lets the compiler check `settle_order.delay(...)`
    at the call site and pick the codec for the argument.
    """
    return f"settled {event.order_id}"


# ---------------------------------------------------------------------------
# 2. RQ -- proposed
# ---------------------------------------------------------------------------
#
# RQ is Redis and nothing else: `Queue(connection=...)` takes a redis client
# directly. So it declares no resource of its own -- it *reuses* the persisted
# Redis, and the compiler should see that and not provision a second cluster.
#
# That reuse is the notable part. A `Queue` wrapping a client that was already
# persisted under id "itemCache" is a queue on that cluster. If the author
# wants them separate, they persist a second Redis client with a second id, and
# the difference is visible in the source rather than inferred.
#
# shim: none. The queue is constructed from an already-connected client.
# What the compiler adds is the worker unit (`rq worker mega-emails`) and the
# same argument type-check as Celery.
#
# open question: RQ serialises with pickle by default, which is exactly what
# mega/wire.py argues against. `Queue(serializer=...)` accepts an alternative,
# so the compiler should either set it or refuse -- silently shipping pickle
# across a unit boundary is not a defensible default for generated
# infrastructure.
emails = Queue("mega-emails", connection=cache)


# ---------------------------------------------------------------------------
# 3. Dramatiq -- proposed
# ---------------------------------------------------------------------------
#
# Dramatiq's broker is global process state: `set_broker` installs it, and the
# `@actor` decorator reads it at import. So what gets persisted is the broker,
# and the `set_broker` call stays exactly where the author put it.
#
# shim: returns a RedisBroker (or a RabbitmqBroker for an `amqp://` url)
# pointed at the provisioned resource. The broker is constructed with a url and
# connects lazily, so this is another clean fit.
broker = cloudcc.persist(RedisBroker(url="redis://localhost:6379/2"), id="actorBroker")
dramatiq.set_broker(broker)


@dramatiq.actor(max_retries=3)
def rebuild_search_index(order_id: str) -> None:
    """A dramatiq actor: the same boundary, a third spelling of it."""
    _ = order_id
