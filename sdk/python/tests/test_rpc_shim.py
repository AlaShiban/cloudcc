"""The shim that turns an in-process call into a request.

This is the runtime half of ``cloudcc.remote``. The compiler has already
checked that the function exists and is ``async def``; what is left to get
wrong is everything that happens once the call leaves the process, and none of
it is visible from either program's source:

* an ``async def`` on the far side has to actually be awaited, or the caller
  gets a coroutine object serialised as null;
* a failure on the far side has to arrive as a failure rather than as a value,
  or the caller carries on with a dict where its result should be;
* an argument JSON cannot carry has to be refused on the way out, where the
  caller can see which call did it.

Loaded by path with a stubbed ``_client``, so the whole thing runs with no
boto3, no credentials and no network -- which is also what makes it a test of
the protocol rather than of the AWS SDK.
"""

import asyncio
import importlib.util
import json
import pathlib
import sys
import types

import pytest

SHIM = (
    pathlib.Path(__file__).resolve().parents[3]
    / "internal"
    / "runtime"
    / "py"
    / "templates"
    / "_cloudcc_runtime"
    / "rpc.py"
)

PACKAGE = "_cloudcc_rpc_under_test"


class FakeLambda:
    """Stands in for the boto3 Lambda client.

    Records what was invoked and replays a scripted reply, so a test can say
    what the other unit did without one existing.
    """

    def __init__(self, reply=None, function_error=None):
        self.reply = reply if reply is not None else {}
        self.function_error = function_error
        self.calls = []

    def invoke(self, **kwargs):
        self.calls.append(kwargs)
        body = json.dumps(self.reply).encode("utf-8")
        out = {"Payload": types.SimpleNamespace(read=lambda: body)}
        if self.function_error:
            out["FunctionError"] = self.function_error
        return out


@pytest.fixture
def rpc():
    if not SHIM.is_file():
        pytest.skip(f"{SHIM} not found; this test runs from a cloudcc checkout")

    package = types.ModuleType(PACKAGE)
    package.__path__ = []
    client = types.ModuleType(PACKAGE + "._client")
    client.env = lambda name, capability, id: "deployed-" + id
    client.slug = lambda id: id.upper()
    client.client = lambda service: FakeLambda()
    sys.modules[PACKAGE] = package
    sys.modules[PACKAGE + "._client"] = client

    spec = importlib.util.spec_from_file_location(PACKAGE + ".rpc", SHIM)
    module = importlib.util.module_from_spec(spec)
    # The template tree is embedded wholesale, so a stray .pyc beside it would
    # travel into every compiled bundle.
    previous, sys.dont_write_bytecode = sys.dont_write_bytecode, True
    try:
        spec.loader.exec_module(module)
    finally:
        sys.dont_write_bytecode = previous
        for name in (PACKAGE, PACKAGE + "._client"):
            sys.modules.pop(name, None)
    return module


# ------------------------------------------------------------- the callee


def test_dispatch_awaits_an_async_function(rpc):
    module = types.SimpleNamespace()

    async def quote(items, surge=1):
        await asyncio.sleep(0)
        return {"total": len(items) * surge}

    module.quote = quote
    event = {rpc.CALL_KEY: {"function": "quote", "args": [["a", "b"]], "kwargs": {"surge": 3}}}

    # The value, not a coroutine: a handler that returned the coroutine would
    # serialise as null and the caller would see None. And wrapped, because a
    # reply is always an envelope.
    assert rpc.dispatch(module, event) == {rpc.RESULT_KEY: {"total": 6}}


def test_dispatch_turns_a_failure_into_an_error_envelope(rpc):
    module = types.SimpleNamespace()

    async def quote(items):
        raise ValueError("no such basket")

    module.quote = quote
    event = {rpc.CALL_KEY: {"function": "quote", "args": [[]], "kwargs": {}}}

    result = rpc.dispatch(module, event)
    assert result[rpc.ERROR_KEY]["type"] == "ValueError"
    assert "no such basket" in result[rpc.ERROR_KEY]["message"]


def test_dispatch_refuses_a_private_name(rpc):
    module = types.SimpleNamespace(_secret=lambda: "internal")
    event = {rpc.CALL_KEY: {"function": "_secret", "args": [], "kwargs": {}}}

    result = rpc.dispatch(module, event)
    assert result[rpc.ERROR_KEY]["type"] == "AttributeError"


def test_is_call_distinguishes_a_call_from_other_events(rpc):
    assert rpc.is_call({rpc.CALL_KEY: {"function": "f"}})
    assert not rpc.is_call({"Records": [{"Sns": {}}]})
    assert not rpc.is_call({"requestContext": {"http": {"method": "GET"}}})
    assert not rpc.is_call("not a dict")


# ------------------------------------------------------------- the caller


def test_a_call_sends_the_function_and_arguments(rpc):
    remote = rpc.Remote("pricing", "nomnom-pricing")
    fake = FakeLambda(reply={rpc.RESULT_KEY: {"total": 12}})
    remote._lambda = fake

    assert asyncio.run(remote.quote(["a"], surge=2)) == {"total": 12}

    sent = json.loads(fake.calls[0]["Payload"])
    assert fake.calls[0]["FunctionName"] == "nomnom-pricing"
    assert sent[rpc.CALL_KEY] == {
        "function": "quote",
        "args": [["a"]],
        "kwargs": {"surge": 2},
    }


def test_an_error_envelope_is_raised_rather_than_returned(rpc):
    remote = rpc.Remote("pricing", "nomnom-pricing")
    remote._lambda = FakeLambda(
        reply={rpc.ERROR_KEY: {"type": "ValueError", "message": "no such basket"}}
    )

    with pytest.raises(rpc.RemoteError) as caught:
        asyncio.run(remote.quote([]))

    # Which unit failed and what it said. Not the original class: this bundle
    # does not carry the other unit's code and has nothing to reconstruct.
    assert caught.value.unit == "pricing"
    assert caught.value.function == "quote"
    assert "no such basket" in str(caught.value)


def test_a_crash_on_the_far_side_is_raised(rpc):
    remote = rpc.Remote("pricing", "nomnom-pricing")
    remote._lambda = FakeLambda(
        reply={"errorType": "KeyError", "errorMessage": "'id'"},
        function_error="Unhandled",
    )

    with pytest.raises(rpc.RemoteError) as caught:
        asyncio.run(remote.quote([]))
    assert "KeyError" in str(caught.value)


def test_an_unserialisable_argument_is_refused_before_it_is_sent(rpc):
    remote = rpc.Remote("pricing", "nomnom-pricing")
    fake = FakeLambda()
    remote._lambda = fake

    with pytest.raises(TypeError) as caught:
        asyncio.run(remote.quote(object()))

    assert "pricing" in str(caught.value)
    assert "JSON" in str(caught.value)
    assert fake.calls == [], "nothing should have been sent"


def test_private_attributes_are_not_callable_over_the_wire(rpc):
    remote = rpc.Remote("pricing", "nomnom-pricing")
    with pytest.raises(AttributeError):
        remote._internal


def test_connect_reads_the_function_name_from_the_environment(rpc):
    remote = rpc.connect("pricing")
    assert remote.id == "pricing"
    assert remote._function == "deployed-pricing"


def test_a_bare_scalar_survives_the_round_trip(rpc):
    """The reason a reply is an envelope at all.

    A function returning a string used to put a bare scalar on the wire, and
    whether it arrived quoted turned out to depend on the runtime rather than
    on the program -- so the same code worked on one Lambda implementation and
    failed on another with "the reply was not JSON: rex (dog)". Wrapping it
    makes the reply self-describing everywhere.
    """
    module = types.SimpleNamespace()

    async def describe(pet):
        return "%s (%s)" % (pet["name"], pet["species"])

    module.describe = describe
    event = {rpc.CALL_KEY: {"function": "describe", "args": [{"name": "rex", "species": "dog"}]}}
    reply = rpc.dispatch(module, event)
    assert reply == {rpc.RESULT_KEY: "rex (dog)"}

    remote = rpc.Remote("pricing", "fn")
    remote._lambda = FakeLambda(reply=reply)
    assert asyncio.run(remote.describe({})) == "rex (dog)"


def test_returning_none_is_not_the_same_as_answering_nothing(rpc):
    remote = rpc.Remote("pricing", "fn")
    remote._lambda = FakeLambda(reply={rpc.RESULT_KEY: None})
    assert asyncio.run(remote.quote([])) is None

    empty = rpc.Remote("pricing", "fn")
    empty._lambda = FakeLambda(reply=None)
    empty._lambda.reply = None
    # An empty payload is a failure, not a null return.
    empty._lambda = _EmptyLambda()
    with pytest.raises(rpc.RemoteError) as caught:
        asyncio.run(empty.quote([]))
    assert "empty" in str(caught.value)


class _EmptyLambda:
    """A runtime that answered with no payload at all."""

    def invoke(self, **kwargs):
        return {"Payload": types.SimpleNamespace(read=lambda: b"")}


def test_a_reply_from_something_else_is_refused(rpc):
    """A unit answering a shape this caller was not compiled for."""
    remote = rpc.Remote("pricing", "fn")
    remote._lambda = FakeLambda(reply={"statusCode": 200, "body": "hello"})
    with pytest.raises(rpc.RemoteError) as caught:
        asyncio.run(remote.quote([]))
    assert "neither a result nor an error" in str(caught.value)
