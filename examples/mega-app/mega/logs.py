"""Logging -- no client to declare, but a destination to choose.

Nothing here is wrapped in `persist`, because there is nothing to wrap: these
libraries write to a stream, and which stream that is has always been decided
by the environment rather than by the program. What the compiler adds is that
the environment stops being implicit.

    logging:
      type: cloudwatch
      retention_days: 14

`cloudwatch` is the only value that works today. `datadog` and `honeycomb` are
recognised and rejected -- with a message saying so -- rather than ignored,
which is the same treatment `eks` gets under `execution_units` and for the same
reason: a configuration key that is silently dropped is worse than one that is
refused, because the program looks configured and is not.

Where a vendor plugs in, when there is one, is the *destination*, not the call
sites. Everything below stays exactly as written; what changes is where the
runtime points the handler it installs, and what the compiler provisions to
receive it -- a log group and a retention policy for CloudWatch, a forwarder
and an API key for a vendor. That is the seam, and it is narrow on purpose: a
logging integration that required application code to change would not be worth
having.
"""

import logging

import structlog
from loguru import logger as loguru_logger

import cloudcompiler as cloudcc

# shim: the injected runtime configures the root logger during unit init,
# before any user module is imported:
#
#   - a single handler on stdout at the level from the config value
#     `log_level`. Lambda and Fargate both capture stdout, so for the
#     CloudWatch destination nothing further is needed -- and a vendor
#     destination is a different handler installed here, not a different call
#     below;
#   - the formatter emits JSON with the request id, the unit id and the trace
#     id already attached, so a line logged from a shared module is
#     attributable to the unit that logged it;
#   - `logging.getLogger()` therefore works unchanged. The calls in this module
#     are what a developer writes when they want more than that.
#
# Uncompiled, the SDK does the same configuration with a human-readable
# formatter, because a terminal is not CloudWatch.
log = logging.getLogger("mega")


# structlog is configured by the application, not by the runtime, because its
# processor chain is a real design choice. What the runtime supplies is the
# last processor: whatever renders the event dict for the configured
# destination. Uncompiled that is a console renderer; compiled for CloudWatch
# it is JSON; compiled for a vendor it is whatever that vendor ingests.
#
# shim: `cloudcc.log_renderer()` returns the right final processor for the
# environment, so this chain is written once and reads correctly everywhere.
# Without it every application grows an `if IS_LAMBDA` branch, which is exactly
# the environment-sniffing the compiler exists to delete -- and it is also
# where a vendor integration would otherwise have to reach in.
structlog.configure(
    processors=[
        structlog.contextvars.merge_contextvars,
        structlog.processors.add_log_level,
        structlog.processors.TimeStamper(fmt="iso"),
        cloudcc.log_renderer(),
    ],
)
slog = structlog.get_logger("mega")


# loguru replaces the handler set on import and writes to stderr by default.
# That is fine in both environments, so the only thing worth doing is to make
# the format match the other two.
#
# shim: the injected runtime removes loguru's default sink and installs one
# that serialises to the configured destination. It does that only if loguru is
# importable -- the runtime must not require a logging library the program did
# not ask for.
loguru_logger.remove()
loguru_logger.add(lambda m: print(m, end=""), serialize=True)
