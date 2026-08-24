"""Relational database backed by RDS.

``connect`` returns a real SQLAlchemy ``Engine``. The program declared one by
calling ``create_engine``, and it gets one back -- pointed at RDS, with the
managed password spliced in.

The URL delivered in the environment carries no password: AWS manages the
master credential, and the compiler passes the managed secret's ARN separately
so nothing sensitive ever sits in an environment variable.
"""

import json
import os
from urllib.parse import quote

from . import _client


def connect(id):
    """Return a SQLAlchemy Engine connected to the database declared for ``id``."""
    from sqlalchemy import create_engine

    return create_engine(url(id))


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
