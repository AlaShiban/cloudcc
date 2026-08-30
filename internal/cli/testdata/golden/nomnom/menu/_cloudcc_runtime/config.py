"""Runtime configuration values, delivered as environment variables."""

import os

from . import _client


def value(id, default="", secret=False):
    """Return the configured value for ``id``.

    ``secret`` is a compile-time signal -- it decides whether the generated
    stack stores the value as a Pulumi secret -- and has no effect on the read.
    """
    return os.environ.get("CLOUDCC_CONFIG_%s" % _client.slug(id), default)
