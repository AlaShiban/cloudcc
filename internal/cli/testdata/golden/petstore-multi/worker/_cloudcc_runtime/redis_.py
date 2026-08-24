"""Cache backed by ElastiCache or MemoryDB, over the Redis protocol."""

import os

from . import _client


def connect(id):
    """Return a client for the cache declared as ``persist_redis(id)``."""
    slug = _client.slug(id)
    host = _client.env("CLOUDCC_REDIS_%s_ENDPOINT" % slug, "persist_redis", id)
    port = int(_client.env("CLOUDCC_REDIS_%s_PORT" % slug, "persist_redis", id))
    tls = os.environ.get("CLOUDCC_REDIS_%s_TLS" % slug, "") == "true"

    import redis as _redis

    return Redis(id, _redis.Redis(host=host, port=port, ssl=tls, decode_responses=True))


class Redis:
    def __init__(self, id, conn):
        self.id = id
        self._conn = conn

    def get(self, key):
        """Return the value at ``key``, or None."""
        return self._conn.get(str(key))

    def set(self, key, value, ex=None):
        """Store ``value`` at ``key``, optionally expiring after ``ex`` seconds."""
        self._conn.set(str(key), str(value), ex=ex)

    def delete(self, key):
        """Remove ``key`` if present."""
        self._conn.delete(str(key))

    def incr(self, key, amount=1):
        """Increment ``key`` and return the new value."""
        return int(self._conn.incrby(str(key), amount))

    def __repr__(self):
        return "<Redis %r (elasticache)>" % self.id
