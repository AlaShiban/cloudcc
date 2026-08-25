"""Logging, configured before any user module is imported.

This is the one capability with no client to hand back. A program does not
choose where its logs go -- an operator does, in cloudcc.yaml -- and the call
sites are identical either way::

    logging.getLogger(__name__).info("started")

So what the runtime owes an application is that by the time that line runs, the
root logger is already pointed at the configured destination, at the configured
level, with the unit's identity attached. Anything less and every application
grows the same twenty lines of handler setup, written slightly differently each
time.

CloudWatch is the only destination implemented. Choosing another is a compile
error naming this one, rather than a key that is quietly dropped -- see
`typeSupport` in internal/provider/aws/support.go.

**Where a vendor plugs in is here**, and nowhere else. A Datadog or Honeycomb
destination is a different handler installed by ``configure``; it is not a
different call in the application, not a decorator, and not a wrapper around
the logger. That narrowness is the whole value of routing this through the
compiler: an integration that required application code to change would not be
worth having.
"""

import json
import logging
import os
import sys

#: Set by the compiler on every unit. See aws.EnvLogDestination.
DESTINATION_ENV = "CLOUDCC_LOG_DESTINATION"

#: Also set by the compiler. A module shared between units cannot know which
#: unit is running it, so the environment says.
UNIT_ENV = "CLOUDCC_UNIT"


def destination():
    """Where this unit's logs are meant to go."""
    return os.environ.get(DESTINATION_ENV, "cloudwatch")


def unit():
    """Which execution unit this process is. Bound by the compiler."""
    return os.environ.get(UNIT_ENV, "")


def configure(unit=""):
    """Install the handler for the configured destination.

    Called from the generated entrypoint before the application module is
    imported, so a log line emitted at import time is already formatted and
    already attributable.
    """
    where = destination()
    if where != "cloudwatch":
        # The compiler rejects this, so reaching it means the bundle and the
        # configuration disagree -- worth failing loudly rather than logging
        # somewhere nobody is looking.
        raise RuntimeError(
            "%s=%r, but this runtime only implements cloudwatch. "
            "The bundle is older than the configuration that deployed it."
            % (DESTINATION_ENV, where)
        )

    root = logging.getLogger()
    # Lambda installs its own handler on the root logger, which formats lines
    # its own way and would double every record. Ours replaces it.
    for existing in list(root.handlers):
        root.removeHandler(existing)

    handler = logging.StreamHandler(sys.stdout)
    handler.setFormatter(_JSONFormatter(unit))
    root.addHandler(handler)
    root.setLevel(_level())
    return root


def _level():
    name = os.environ.get("CLOUDCC_CONFIG_LOG_LEVEL", "info").upper()
    return getattr(logging, name, logging.INFO)


class _JSONFormatter(logging.Formatter):
    """One JSON object per line.

    CloudWatch parses these into queryable fields, and a line logged from a
    module shared between units still says which unit logged it -- which is the
    fact a shared module cannot know about itself.
    """

    def __init__(self, unit):
        super().__init__()
        self._unit = unit

    def format(self, record):
        payload = {
            "level": record.levelname.lower(),
            "logger": record.name,
            "message": record.getMessage(),
        }
        if self._unit:
            payload["unit"] = self._unit
        request_id = os.environ.get("_X_AMZN_TRACE_ID")
        if request_id:
            payload["trace"] = request_id
        if record.exc_info:
            payload["exception"] = self.formatException(record.exc_info)
        return json.dumps(payload, sort_keys=True)
