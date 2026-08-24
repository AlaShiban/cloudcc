"""Cache backed by ElastiCache or MemoryDB.

``connect`` returns a real ``redis.Redis``. The program declared one by
constructing one, and what it gets back is the same type pointed at the
provisioned cluster -- so every method, every option and every type stub the
library ships still applies. There is no wrapper here to drift from it.
"""

import os

from . import _client


def connect(id):
    """Return a redis.Redis connected to the cache declared for ``id``."""
    slug = _client.slug(id)
    host = _client.env("CLOUDCC_REDIS_%s_ENDPOINT" % slug, "persist", id)
    port = int(_client.env("CLOUDCC_REDIS_%s_PORT" % slug, "persist", id))
    tls = os.environ.get("CLOUDCC_REDIS_%s_TLS" % slug, "") == "true"

    import redis as _redis

    return _redis.Redis(host=host, port=port, ssl=tls, decode_responses=True)
