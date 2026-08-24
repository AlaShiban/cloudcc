"""Runtime clients injected by cc into the compiled copy of your application.

Your source never imports boto3. The compiler rewrites each SDK hint call in
*a copy* of your code into a call on one of these modules, and only these
modules talk to AWS.

Every client honours CC_AWS_ENDPOINT_URL, so a compiled application can be
pointed at an AWS emulator with no code change at all -- which is how the
integration tests run it.

The public methods here mirror the SDK's local emulations exactly. A parity
test in the compiler's suite compares the two signature by signature, because
two implementations of one API drift otherwise.
"""

__all__ = ["config", "expose", "fs", "kv", "orm", "pubsub", "redis_", "secret"]
