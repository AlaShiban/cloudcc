"""Runtime clients injected by cloudcc into the compiled copy of your application.

Your source never imports boto3. The compiler rewrites each SDK hint call in
*a copy* of your code into a call on one of these modules, and only these
modules talk to AWS.

Every client honours CLOUDCC_AWS_ENDPOINT_URL, so a compiled application can be
pointed at an AWS emulator with no code change at all -- which is how the
integration tests run it.

The public methods here mirror the SDK's local emulations exactly. A parity
test in the compiler's suite compares the two signature by signature, because
two implementations of one API drift otherwise.

Importing this package configures logging for the configured destination. That
is a side effect, which is normally bad manners -- but it is the only point
that is guaranteed to run before the application's own modules, in every unit,
under Lambda and under uvicorn alike. A rewritten module imports one of these
clients at the top of the file, so by the time its first statement executes the
root logger is already pointed at the right place.
"""

from . import logs as _logs

__all__ = ["config", "expose", "fs", "kv", "logs", "orm", "pubsub", "redis_", "secret"]

_logs.configure(_logs.unit())
