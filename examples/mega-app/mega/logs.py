"""Logging -- the category with no resources and no hints at all.

Nothing here is declared, because there is nothing to provision: the unit
already has a log destination by virtue of being a unit. What this category
needs is *configuration that happens before user code runs*, which is the one
thing an application module cannot arrange for itself.

So this file is a statement of what the injected runtime should have already
done by the time it is imported, written as the configuration a developer would
otherwise have to write by hand in every unit.
"""

import logging

import structlog
from loguru import logger as loguru_logger

# shim: the injected runtime configures the root logger during unit init,
# before any user module is imported:
#
#   - stdlib logging gets a single StreamHandler on stdout at the level from
#     the config value `log_level`. Lambda and Fargate both capture stdout, so
#     nothing needs to know it is running in the cloud;
#   - the formatter emits JSON with the request id, the unit id and the trace
#     id already attached, so a line logged from a shared module is attributable
#     to the unit that logged it;
#   - `logging.getLogger()` therefore works unchanged. This module's own calls
#     below are what a developer writes when they want more than that.
#
# Uncompiled, the SDK does the same configuration with a human-readable
# formatter, because a terminal is not CloudWatch.
log = logging.getLogger("mega")


# structlog is configured by the application, not by the runtime, because its
# processor chain is a real design choice. What the runtime supplies is the last
# processor: whatever renders the event dict. Uncompiled that is a console
# renderer; compiled it is JSON.
#
# shim: `cloudcc.log_renderer()` (proposed) returns the right final processor
# for the environment, so this chain is written once and reads correctly in
# both. Without it every application ends up with an `if IS_LAMBDA` branch,
# which is exactly the sort of environment-sniffing the compiler exists to
# delete.
structlog.configure(
    processors=[
        structlog.contextvars.merge_contextvars,
        structlog.processors.add_log_level,
        structlog.processors.TimeStamper(fmt="iso"),
        structlog.processors.JSONRenderer(),
    ],
)
slog = structlog.get_logger("mega")


# loguru replaces the handler set on import and writes to stderr by default.
# That is fine in both environments, so the only thing worth doing is to make
# the format match the other two.
#
# shim: the injected runtime removes loguru's default sink and installs one
# that serialises, so a loguru line and a stdlib line are the same shape in
# CloudWatch. It does that only if loguru is importable -- the runtime must not
# require a logging library the program did not ask for.
loguru_logger.remove()
loguru_logger.add(lambda m: print(m, end=""), serialize=True)
