"""Key/value store backed by DynamoDB.

Values are stored JSON-encoded under a single ``value`` attribute so that any
JSON-shaped dict round-trips unchanged. Storing the dict as native DynamoDB
attributes instead would quietly turn numbers into Decimals and reject empty
values, which is not what the local emulation does.
"""

import json

from . import _client


def connect(id):
    """Return a client for the KV store declared as ``persist(KVStore(), id=...)``."""
    name = _client.env("CLOUDCC_KV_%s_TABLE" % _client.slug(id), "persist", id)
    return KVStore(id, _client.resource("dynamodb").Table(name))


class KVStore:
    def __init__(self, id, table):
        self.id = id
        self._table = table

    def get(self, key):
        """Return the item at ``key``, or None."""
        item = self._table.get_item(Key={"id": str(key)}).get("Item")
        if item is None:
            return None
        return json.loads(item["value"])

    def put(self, key, value):
        """Store ``value`` at ``key``."""
        self._table.put_item(Item={"id": str(key), "value": json.dumps(value)})

    def delete(self, key):
        """Remove ``key`` if present."""
        self._table.delete_item(Key={"id": str(key)})

    def keys(self):
        """Every key currently stored, sorted."""
        out = []
        kwargs = {"ProjectionExpression": "id"}
        while True:
            page = self._table.scan(**kwargs)
            out.extend(item["id"] for item in page.get("Items", []))
            token = page.get("LastEvaluatedKey")
            if not token:
                return sorted(out)
            kwargs["ExclusiveStartKey"] = token

    def __repr__(self):
        return "<KVStore %r (dynamodb)>" % self.id
