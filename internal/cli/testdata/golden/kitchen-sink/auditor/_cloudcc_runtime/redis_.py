"""Cache backed by ElastiCache or MemoryDB.

``connect`` returns a real Redis client of the same kind the program declared:
``redis.Redis`` gets a ``redis.Redis`` back and ``redis.asyncio.Redis`` gets an
``redis.asyncio.Redis``. What comes back is the same type pointed at the
provisioned cluster -- so every method, every option and every type stub the
library ships still applies. There is no wrapper here to drift from it.

Handing back the synchronous client either way would compile cleanly and then
raise ``TypeError: object bool can't be used in 'await' expression`` on the
first call, which is the failure the library argument exists to prevent.
"""

import os

from . import _client, trace


def connect(id, library="redis-py"):
    """Return a Redis client connected to the cache declared for ``id``."""
    slug = _client.slug(id)
    host = _client.env("CLOUDCC_REDIS_%s_ENDPOINT" % slug, "persist", id)
    port = int(_client.env("CLOUDCC_REDIS_%s_PORT" % slug, "persist", id))
    tls = os.environ.get("CLOUDCC_REDIS_%s_TLS" % slug, "") == "true"

    if library == "redis-py-async":
        import redis.asyncio as _redis
    else:
        import redis as _redis

    # Only the connection settings are supplied here. Every other option is
    # left at the library's default, because the program's own client had them
    # at the library's default too and this one is meant to be the same client
    # pointed somewhere else.
    #
    # `decode_responses=True` used to be set here. Nothing needed it and it
    # made `get` answer with `str` where the uncompiled program's client
    # answers with `bytes` -- so a program that called `.decode()` on the
    # result worked locally and raised `AttributeError: 'str' object has no
    # attribute 'decode'` once deployed. It also contradicted this module's own
    # docstring, which promises every option the library ships still applies.
    # The seam trace in tests/e2e/examples.sh is what finally caught it; no
    # response comparison could, because the one example that hit it happened
    # to guard with isinstance.
    return trace.wrap(_redis.Redis(host=host, port=port, ssl=tls), "redis", id)
