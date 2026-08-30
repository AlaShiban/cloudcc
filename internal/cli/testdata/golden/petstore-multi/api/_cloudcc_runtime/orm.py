"""Relational database backed by RDS.

``connect`` returns a real SQLAlchemy engine of the same kind the program
declared: ``create_engine`` gets an ``Engine`` back and ``create_async_engine``
gets an ``AsyncEngine``. Handing back the synchronous one either way would
compile cleanly and then fail on the first ``async with``.

The URL delivered in the environment carries no password: AWS manages the
master credential, and the compiler passes the managed secret's ARN separately
so nothing sensitive ever sits in an environment variable.
"""

import json
import os
from urllib.parse import quote

from . import _client, trace


def connect(id, library="sqlalchemy"):
    """Return a SQLAlchemy engine connected to the database declared for ``id``."""
    if library == "sqlalchemy-async":
        from sqlalchemy.ext.asyncio import create_async_engine

        return trace.wrap(create_async_engine(_async_url(id)), "orm", id)

    from sqlalchemy import create_engine

    return trace.wrap(create_engine(url(id)), "orm", id)


def _async_url(id):
    """The connection URL with an async driver, which asyncio requires.

    A URL good for ``create_engine`` names a synchronous driver, and
    ``create_async_engine`` refuses it. The program asked for async by the
    function it called, so the driver is swapped to match rather than making
    the user spell it twice.
    """
    raw = url(id)
    for sync, async_ in (("postgresql://", "postgresql+asyncpg://"),
                         ("postgresql+psycopg2://", "postgresql+asyncpg://"),
                         ("mysql://", "mysql+aiomysql://"),
                         ("mysql+pymysql://", "mysql+aiomysql://")):
        if raw.startswith(sync):
            return async_ + raw[len(sync):]
    return raw


def url(id):
    """The connection URL, with the managed password spliced in."""
    slug = _client.slug(id)
    base = _client.env("CLOUDCC_ORM_%s_URL" % slug, "persist", id)
    arn = os.environ.get("CLOUDCC_ORM_%s_SECRET_ARN" % slug)
    if not arn:
        return base

    raw = _client.client("secretsmanager").get_secret_value(SecretId=arn)
    password = json.loads(raw["SecretString"])["password"]
    scheme, rest = base.split("://", 1)
    user, host = rest.split("@", 1)
    return "%s://%s:%s@%s" % (scheme, user, quote(password, safe=""), host)
