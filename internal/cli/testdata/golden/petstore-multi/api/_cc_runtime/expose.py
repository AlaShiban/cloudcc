"""Handle returned by a rewritten ``cc.expose`` call."""

import os

from . import _client


def register(app, id="main", target="public"):
    """Record the exposed application and return its gateway handle.

    The application object itself is what the generated Lambda entrypoint
    imports; this call exists so the gateway's deployed URL is reachable from
    inside the program.
    """
    return Gateway(id, target, app)


class Gateway:
    def __init__(self, id, target="public", app=None):
        self.id = id
        self.target = target
        self.app = app

    def url(self):
        """The deployed URL of this gateway."""
        return os.environ.get("CC_GATEWAY_%s_URL" % _client.slug(self.id), "")

    def __repr__(self):
        return "<Gateway %r (apigateway)>" % self.id
