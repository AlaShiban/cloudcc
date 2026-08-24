"""Relational database handle backed by RDS."""

import json
import os
from urllib.parse import quote

from . import _client


def connect(id, models=None):
    """Return a handle for the database declared as ``persist_orm(id)``.

    The URL delivered in the environment carries no password: AWS manages the
    master credential, and the compiler passes the managed secret's ARN
    separately so nothing sensitive ever sits in an environment variable.
    """
    slug = _client.slug(id)
    url = _client.env("CC_ORM_%s_URL" % slug, "persist_orm", id)
    arn = os.environ.get("CC_ORM_%s_SECRET_ARN" % slug)
    return OrmSession(id, url, arn)


class OrmSession:
    def __init__(self, id, url, secret_arn=None):
        self.id = id
        self._url = url
        self._secret_arn = secret_arn
        self._resolved = None

    def url(self):
        """The database connection URL, with the managed password spliced in."""
        if self._resolved is not None:
            return self._resolved
        if not self._secret_arn:
            self._resolved = self._url
            return self._resolved

        raw = _client.client("secretsmanager").get_secret_value(SecretId=self._secret_arn)
        password = json.loads(raw["SecretString"])["password"]
        scheme, rest = self._url.split("://", 1)
        user, host = rest.split("@", 1)
        self._resolved = "%s://%s:%s@%s" % (scheme, user, quote(password, safe=""), host)
        return self._resolved

    def __repr__(self):
        return "<OrmSession %r (rds)>" % self.id
