"""Key/value store backed by DynamoDB.

There is no class here, and that is the whole design. ``connect`` returns the
same object the uncompiled program held -- a ``boto3`` ``Table`` -- bound to
the provisioned table instead of the local one.

Which means ``put_item``, ``query``, ``update_item``, ``batch_writer`` and the
paginators all work, and go on working when boto3 adds to them, because they
*are* boto3's. A class of our own would have supported the four methods someone
thought of, in a dialect nobody else speaks, and would have had to be kept in
step with the SDK's local emulation forever.

The cost is that the uncompiled program needs a real DynamoDB endpoint. That
was already true of every other store: redis-py wants a Redis, SQLAlchemy wants
a Postgres. The key/value store has stopped being the exception.
"""

from . import _client


def connect(id):
    """Return the Table declared as ``persist(boto3...Table(...), id=...)``."""
    name = _client.env("CLOUDCC_KV_%s_TABLE" % _client.slug(id), "persist", id)
    return _client.resource("dynamodb").Table(name)
