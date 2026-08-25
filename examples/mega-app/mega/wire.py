"""What goes over the wire between execution units.

The question this file answers: can Pydantic, Marshmallow or msgspec serve as
the codec for pub/sub messages and for calls between units, rather than
cloudcc inventing a format of its own?

**Yes for all three, but not on equal terms**, and the difference decides the
design:

===============  ====================================  =========================
library          encode / decode                       what the decoder needs
===============  ====================================  =========================
msgspec          ``json.encode`` / ``json.decode``      the type, passed in
pydantic         ``model_dump_json`` / ``validate_json`` the model class
marshmallow      ``schema.dumps`` / ``schema.loads``     *a schema instance*
===============  ====================================  =========================

The first two are recoverable **from the type alone**. That matters because the
publisher and the subscriber are different processes in different units, and
the only thing they provably share is the source tree they were compiled from.
If the codec is a function of the type, the compiler can derive it at both ends
and guarantee they agree. Marshmallow's schema is a separate object -- one type
may have several schemas -- so with marshmallow the codec must be *named* at
the declaration site.

Hence the rule this example is written against:

    **The codec is a property of the channel, not of the call site.** It is
    fixed once, where the channel is declared, and both ends read it from the
    same place. A publisher and a subscriber disagreeing about the format is a
    compile error, not a poison message at three in the morning.

Which is why the topic below is *typed*: ``Topic[OrderPlaced]``. The type
parameter is the codec.

None of the three can encode arbitrary Python objects, and that is a feature.
The alternative is pickle, which turns every subscriber into a remote code
execution sink, and which breaks the moment two units are built from slightly
different trees. A payload that cannot be described by a schema is a payload
that should not be crossing a unit boundary.
"""

from __future__ import annotations

import datetime as dt
from decimal import Decimal

import marshmallow
import msgspec
from pydantic import BaseModel


class OrderPlaced(BaseModel):
    """A pydantic event. The class is the schema, so the type is enough."""

    order_id: str
    customer_id: str
    total: Decimal
    placed_at: dt.datetime


class ShipmentRequested(msgspec.Struct):
    """A msgspec event.

    msgspec is the cheapest of the three by a wide margin, decodes straight
    into the struct without an intermediate dict, and needs no import of the
    producer's module beyond the type itself. For a channel whose only job is
    to move a small record between two units, it is the right default.
    """

    order_id: str
    warehouse: str
    requested_at: dt.datetime


class RefundRequested(msgspec.Struct):
    order_id: str
    reason: str
    amount: Decimal


class RefundRequestedSchema(marshmallow.Schema):
    """A marshmallow schema, for the case where validation is the point.

    Marshmallow earns its place when the payload arrives from outside and the
    rules are richer than the types -- ``validate=``, ``@validates_schema``,
    conditional requiredness. The cost is that the schema is a separate object,
    so a channel using it has to say which schema it means.
    """

    order_id = marshmallow.fields.Str(required=True)
    reason = marshmallow.fields.Str(
        required=True, validate=marshmallow.validate.OneOf(["damaged", "late", "other"])
    )
    amount = marshmallow.fields.Decimal(required=True, places=2)


# ---------------------------------------------------------------------------
# What the compiler and the shim do with the above
# ---------------------------------------------------------------------------
#
# shim: the pub/sub shim carries a codec pair, chosen once per topic:
#
#     publish(value)  ->  bytes  ->  SNS/SQS/Kafka
#     bytes           ->  value  ->  the handler
#
#   - for a ``Topic[T]`` where T is a pydantic model: ``T.model_validate_json``
#     and ``value.model_dump_json()``;
#   - for a ``Topic[T]`` where T is a msgspec.Struct, dataclass or builtin:
#     ``msgspec.json.decode(data, type=T)`` and ``msgspec.json.encode(value)``;
#   - for a topic declared with an explicit codec (see mega/messaging.py), the
#     named schema's ``loads``/``dumps``.
#
# The compiler:
#
#   - records the codec and the fully-qualified type on the topic's intent, so
#     both the publishing unit and the subscribing unit resolve the same pair
#     from the IR rather than each inferring one locally;
#   - checks the subscriber's handler annotation against the topic's type, and
#     refuses to compile a handler annotated ``ShipmentRequested`` subscribed
#     to a ``Topic[OrderPlaced]``. This is the single highest-value check in
#     the whole messaging story: it is the bug that survives every unit test,
#     because the two sides are never in the same test;
#   - ensures the module defining the type travels with *both* units, even when
#     only one of them imports it directly. A subscriber that decodes into a
#     class it does not otherwise reference still needs the class;
#   - keeps the same codec for a task queue argument list (mega/jobs.py), which
#     is the same problem wearing different clothes.
#
# open question: schema evolution. Adding an optional field is safe in all
# three libraries; removing a required one is not, and the two units are not
# deployed at the same instant. The compiler knows both versions of the type
# at compile time and could refuse a change that a message in flight would not
# survive. Worth doing, and out of scope for this example.
