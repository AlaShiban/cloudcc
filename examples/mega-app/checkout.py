"""Unit "checkout" -- Flask on Lambda behind its own HTTP API.

Flask is the middle case. The application is a single object like FastAPI's, so
`expose` is unambiguous; the routes are on decorators, so they are
discoverable. The one difference that matters is that Flask is WSGI, not ASGI,
so the generated entry needs an adapter of a different shape.

shim: the Lambda entry wraps this app with a WSGI adapter rather than Mangum.
The compiler picks which by looking at what was exposed, which it must do
anyway to know whether the handlers may be async.
"""

from flask import Flask, jsonify, request
from marshmallow import ValidationError
from sqlmodel import Session, select

import cloudcompiler as cloudcc

from mega.drivers import lookup
from mega.jobs import emails, rebuild_search_index
from mega.orm import checkout_engine
from mega.settings import settings
from mega.wire import RefundRequestedSchema

cloudcc.execution_unit(id="checkout")

app = Flask(__name__)
cloudcc.expose(app, id="checkout-api")

_refund_schema = RefundRequestedSchema()


@app.get("/health")
def health():
    return jsonify(status="ok", timeout=settings.checkout_timeout_seconds)


@app.post("/refunds")
def refund():
    """Marshmallow doing what marshmallow is for: validating outside input.

    The schema is named here *and* on the topic in mega/messaging.py, and they
    are the same object -- which is the constraint that makes marshmallow
    usable as a wire codec at all. See mega/wire.py.
    """
    try:
        payload = _refund_schema.load(request.get_json())
    except ValidationError as err:
        return jsonify(errors=err.messages), 400

    emails.enqueue(_notify, payload["order_id"])
    rebuild_search_index.send(payload["order_id"])
    return jsonify(ok=True, order_id=payload["order_id"])


@app.get("/orders/<order_id>")
def order(order_id: str):
    """SQLModel through a plain SQLAlchemy Session."""
    from mega.models import Order  # a SQLModel table

    with Session(checkout_engine) as session:
        found = session.exec(select(Order).where(Order.id == order_id)).first()
    if found is None:
        return jsonify(error="no such order"), 404
    return jsonify(id=found.id, total=str(found.total))


@app.get("/regions/<postcode>")
def region(postcode: str):
    """The unpersisted sqlite3 cache from mega/drivers.py, used as intended."""
    row = lookup.execute(
        "SELECT region FROM postcode WHERE code = ?", (postcode,)
    ).fetchone()
    return jsonify(region=row[0] if row else None)


def _notify(order_id: str) -> None:
    """An RQ job. Runs in unit "worker"; see mega/jobs.py."""
    _ = order_id
