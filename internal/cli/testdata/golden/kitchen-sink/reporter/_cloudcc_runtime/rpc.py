"""Calls between execution units, backed by Lambda invoke.

Uncompiled, ``cloudcc.remote(pricing, id="pricing")`` returns the module and
``await pricing.quote(...)`` is an ordinary in-process call. Compiled, this
module stands in for it: the same await serialises its arguments, invokes the
other unit's function, and returns what came back.

The call is synchronous in the sense that matters -- the caller waits for a
value -- and asynchronous in the sense Python means, because the wait is a
network round trip and blocking the event loop through one would stall every
other request the process is serving. boto3 has no async client, so the invoke
runs in a worker thread.

Two things deliberately do not travel: exceptions arrive as a description
rather than as the original class, because the caller's bundle does not carry
the callee's code and cannot reconstruct one; and anything JSON cannot carry is
rejected on the way out rather than silently reshaped.
"""

import asyncio
import json

from . import _client

#: Envelope keys. The compiler's entry template and this module are the two
#: ends of one protocol, so the spellings live here and are imported there.
CALL_KEY = "cloudcc_call"
ERROR_KEY = "cloudcc_error"


def connect(id):
    """Return a client for the execution unit declared as remote(id=...)."""
    name = _client.env(
        "CLOUDCC_UNIT_%s_FUNCTION" % _client.slug(id), "remote", id
    )
    return Remote(id, name)


class Remote:
    """A stand-in for another unit's module.

    Attribute access returns a coroutine function, so the call site is
    unchanged from the uncompiled program: ``await pricing.quote(basket)``.
    Which functions exist was checked at compile time against the callee's
    source, which is why there is no table of them here.
    """

    def __init__(self, id, function_name):
        self.id = id
        self._function = function_name
        self._lambda = None

    def __getattr__(self, name):
        if name.startswith("_"):
            # Private names are not part of a unit's interface, and letting
            # them through here would turn a typo into a request.
            raise AttributeError(name)

        async def call(*args, **kwargs):
            return await asyncio.to_thread(self._invoke, name, args, kwargs)

        call.__name__ = name
        call.__qualname__ = "%s.%s" % (self.id, name)
        return call

    def _invoke(self, function, args, kwargs):
        """One blocking round trip. Runs in a worker thread."""
        if self._lambda is None:
            self._lambda = _client.client("lambda")

        try:
            payload = json.dumps(
                {CALL_KEY: {"function": function, "args": list(args), "kwargs": kwargs}}
            ).encode("utf-8")
        except (TypeError, ValueError) as exc:
            raise TypeError(
                "the arguments to %s.%s() cannot be sent to execution unit %r: %s. "
                "Arguments cross the wire as JSON, which is what lets the two "
                "units deploy independently" % (self.id, function, self.id, exc)
            ) from None

        response = self._lambda.invoke(
            FunctionName=self._function,
            InvocationType="RequestResponse",
            Payload=payload,
        )
        body = response["Payload"].read()

        # A handler that raised produces FunctionError and a trace, which is a
        # different failure from one that returned an error envelope.
        if response.get("FunctionError"):
            raise RemoteError(self.id, function, _describe(body))

        if not body:
            return None
        try:
            result = json.loads(body)
        except ValueError:
            raise RemoteError(
                self.id, function, "the reply was not JSON: %r" % body[:200]
            ) from None

        if isinstance(result, dict) and ERROR_KEY in result:
            detail = result[ERROR_KEY]
            raise RemoteError(
                self.id,
                function,
                "%s: %s" % (detail.get("type", "Exception"), detail.get("message", "")),
            )
        return result

    def __repr__(self):
        return "<Remote %r (lambda %s)>" % (self.id, self._function)


class RemoteError(RuntimeError):
    """A call reached the other unit and the other unit failed.

    Deliberately not the original exception class: the caller's bundle does not
    carry the callee's code, so there is nothing to raise. What it carries
    instead is which unit failed and what it said, which is what a caller can
    act on -- and it is one class, so `except RemoteError` catches every
    cross-unit failure without importing anything from the other service.
    """

    def __init__(self, unit, function, detail):
        self.unit = unit
        self.function = function
        self.detail = detail
        super().__init__("execution unit %r failed in %s(): %s" % (unit, function, detail))


def _describe(body):
    """Summarise a Lambda error payload for a RemoteError."""
    try:
        parsed = json.loads(body)
    except ValueError:
        return body.decode("utf-8", "replace")[:500]
    if isinstance(parsed, dict):
        return "%s: %s" % (
            parsed.get("errorType", "Exception"),
            parsed.get("errorMessage", ""),
        )
    return str(parsed)[:500]


# ------------------------------------------------------------------ callee


def is_call(event):
    """Whether an invocation is a call from another execution unit."""
    return isinstance(event, dict) and CALL_KEY in event


def dispatch(module, event):
    """Run the requested function on the unit's entry module.

    Called by the generated Lambda entrypoint. The function is resolved by
    name; the compiler has already checked that the name exists and is
    ``async def``, so this failing means the deployed bundle and the compile
    that produced the caller have drifted apart -- which is worth saying
    plainly rather than raising AttributeError.
    """
    request = event[CALL_KEY] or {}
    name = request.get("function", "")
    args = request.get("args") or []
    kwargs = request.get("kwargs") or {}

    if name.startswith("_") or not hasattr(module, name):
        return {
            ERROR_KEY: {
                "type": "AttributeError",
                "message": "this unit has no remote function %r; the caller was "
                "compiled against a different version of it" % name,
            }
        }

    fn = getattr(module, name)
    try:
        result = fn(*args, **kwargs)
        if asyncio.iscoroutine(result):
            # The handler Lambda calls is synchronous, so the loop is this
            # module's to own. Nothing else is running one: a unit answers a
            # single invocation at a time.
            result = asyncio.run(result)
        return result
    except Exception as exc:  # noqa: BLE001 -- the caller gets every failure
        return {ERROR_KEY: {"type": type(exc).__name__, "message": str(exc)}}
