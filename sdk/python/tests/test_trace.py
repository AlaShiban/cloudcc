"""The tracer is the instrument the correctness comparison reads, so it is
worth more scepticism than the things it measures.

Two properties matter, and they pull against each other:

* **It records what happened.** Operation, logical id, arguments, result.
* **It changes nothing.** The runtime hands back the library's own object on
  purpose (see kv.py); a proxy that broke iteration, `with`, `/` or equality
  would break the program. Worse, it would break *both* halves identically and
  the trace diff would still pass -- an instrument that lies quietly is worse
  than no instrument.

The transparency tests below are therefore not padding. They are the half of
this file that stops the other half from being believed too easily.
"""

import asyncio
import datetime
import decimal
import importlib.util
import pathlib
import sys

import pytest

SHIM_DIR = (
    pathlib.Path(__file__).resolve().parents[3]
    / "internal" / "runtime" / "py" / "templates" / "_cloudcc_runtime"
)


def _load(name):
    spec = importlib.util.spec_from_file_location(
        "_cc_" + name, SHIM_DIR / (name + ".py")
    )
    mod = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = mod
    spec.loader.exec_module(mod)
    return mod


trace = _load("trace")


def test_the_sdk_copy_is_byte_identical_to_the_injected_one():
    """Both halves must observe through the same code.

    The uncompiled run traces through `cloudcompiler._trace`; the compiled run
    traces through the injected `_cloudcc_runtime.trace`. If those two drifted,
    a difference in the trace would no longer distinguish "the program did
    something different" from "the instrument did something different" -- and
    the comparison would be measuring itself.

    The SDK cannot import the compiler's template tree at runtime, so the file
    is vendored. This pins the copy.
    """
    injected = (SHIM_DIR / "trace.py").read_bytes()
    vendored = (
        pathlib.Path(__file__).resolve().parents[1]
        / "cloudcompiler" / "_trace.py"
    ).read_bytes()
    assert injected == vendored, (
        "sdk/python/cloudcompiler/_trace.py has drifted from "
        "internal/runtime/py/templates/_cloudcc_runtime/trace.py; copy one over the other"
    )


def test_both_halves_agree_on_the_capability_names():
    """The compiled half records its capability literally -- each shim knows
    what it is -- while the uncompiled half infers one from the client's type.
    If those two vocabularies drifted, every event would differ on `kind` and
    the comparison would report a difference that is not one: the worst kind of
    failure for a correctness check, because it trains you to ignore it.
    """
    import re
    import cloudcompiler

    shim_kinds = set()
    for path in SHIM_DIR.glob("*.py"):
        source = path.read_text()
        shim_kinds |= set(re.findall(r'trace\.wrap\(.*?"([a-z]+)",\s*id\)', source, re.S))
        shim_kinds |= set(re.findall(r'trace\.emit\(\s*"([a-z]+)"', source))

    sdk_kinds = set(cloudcompiler._KIND_BY_ROOT_MODULE.values()) | {"pubsub", "secret"}
    assert shim_kinds == sdk_kinds, (
        "the injected runtime records %s and the SDK records %s"
        % (sorted(shim_kinds), sorted(sdk_kinds))
    )


@pytest.mark.parametrize("module_root, expected", [
    ("redis", "redis"),
    ("sqlalchemy", "orm"),
    ("boto3", "kv"),
])
def test_a_clients_library_decides_its_capability(module_root, expected):
    import cloudcompiler

    class Client:
        pass

    Client.__module__ = module_root + ".submodule"
    assert cloudcompiler._kind_of(Client()) == expected


def test_a_path_is_the_file_store(tmp_path):
    import cloudcompiler
    assert cloudcompiler._kind_of(tmp_path) == "fs"


def test_an_unrecognised_client_is_not_guessed_at():
    # Guessing would make two identical runs differ on `kind`. "unknown" is
    # visible in the trace and identical on both sides of a comparison only if
    # both sides fail to recognise it -- which is the honest outcome.
    import cloudcompiler

    class Homegrown:
        pass

    assert cloudcompiler._kind_of(Homegrown()) == "unknown"


def test_the_cache_shim_does_not_override_the_clients_options():
    """A shim supplies where a client connects, never how it behaves.

    `decode_responses=True` was set here once. Nothing needed it, and it made
    the compiled client answer `get` with `str` where the program's own client
    answers with `bytes` -- so `value.decode("utf-8")` worked locally and
    raised `AttributeError: 'str' object has no attribute 'decode'` once
    deployed. Every response comparison in the suite passed throughout, because
    the one example that reached it guarded with isinstance; the seam trace is
    what caught it.

    The general form -- passing a program's own constructor keyword arguments
    through to the compiled client -- is deliberately *not* done, and
    sdkdetect/hint.go says why: what host a `Redis(host=...)` uses is none of
    the compiler's business. That principle cuts both ways, which is the whole
    point of this test: if the compiler will not read those options, it must
    not invent them either.
    """
    source = (SHIM_DIR / "redis_.py").read_text()
    call = source[source.index("_redis.Redis("):]
    call = call[: call.index(")")]
    assert "decode_responses" not in call, (
        "the cache shim is overriding a client option again; the compiled "
        "client would stop behaving like the one the program constructed"
    )


@pytest.fixture(autouse=True)
def _clean(monkeypatch):
    monkeypatch.delenv(trace.ENV, raising=False)
    trace.reset()
    yield
    trace.reset()


def events(capsys):
    """The trace records emitted so far, decoded."""
    import json
    out = []
    for line in capsys.readouterr().err.splitlines():
        if line.startswith(trace.MARKER):
            out.append(json.loads(line[len(trace.MARKER):].strip()))
    return out


class Store:
    """Stands in for a client library: boto3 Table, redis, an Engine."""

    def __init__(self):
        self.items = {}

    def put(self, key, value):
        self.items[key] = value
        return {"ok": True}

    def get(self, key):
        return self.items[key]

    def boom(self):
        raise KeyError("nope")


# --------------------------------------------------------------------- off

def test_disabled_returns_the_very_same_object():
    store = Store()
    assert trace.wrap(store, "kv", "pets") is store


def test_disabled_emits_nothing(capsys):
    trace.emit("kv", "pets", "put")
    assert events(capsys) == []


# ------------------------------------------------------------- recording

def test_records_operation_arguments_and_result(monkeypatch, capsys):
    monkeypatch.setenv(trace.ENV, "1")
    store = trace.wrap(Store(), "kv", "pets")
    store.put("1", {"name": "rex"})

    (rec,) = events(capsys)
    assert rec["kind"] == "kv"
    assert rec["id"] == "pets"
    assert rec["op"] == "put"
    assert rec["args"]["a"] == ["1", {"name": "rex"}]
    assert rec["ret"] == {"ok": True}


def test_records_the_exception_type_and_re_raises(monkeypatch, capsys):
    monkeypatch.setenv(trace.ENV, "1")
    store = trace.wrap(Store(), "kv", "pets")
    with pytest.raises(KeyError):
        store.boom()

    (rec,) = events(capsys)
    assert rec["op"] == "boom"
    assert rec["err"] == "KeyError"
    assert "ret" not in rec


def test_the_logical_id_is_recorded_not_the_physical_name(monkeypatch, capsys):
    # The whole comparison rests on this: `pets` locally and `petstore-pets`
    # in the cloud must trace identically, or every run would differ for a
    # reason that is not a defect. Recording the id -- and only the id -- is
    # what makes that true while still catching a unit wired to another store.
    monkeypatch.setenv(trace.ENV, "1")
    trace.wrap(Store(), "kv", "pets").put("1", {})
    (rec,) = events(capsys)
    assert rec["id"] == "pets"


def test_a_call_through_a_returned_object_is_still_recorded(monkeypatch, capsys):
    monkeypatch.setenv(trace.ENV, "1")

    class Engine:
        def connect(self):
            return Conn()

    class Conn:
        def execute(self, sql):
            return [(1,)]

        def __enter__(self):
            return self

        def __exit__(self, *a):
            return False

    engine = trace.wrap(Engine(), "orm", "shopdb")
    with engine.connect() as conn:
        conn.execute("SELECT 1")

    ops = [e["op"] for e in events(capsys)]
    # Without following the returned Connection the SQL would be invisible,
    # which is most of what an ORM seam is.
    assert ops == ["connect", "connect.execute"]


# ------------------------------------------------------- canonicalisation

def test_decimal_compares_as_a_number(monkeypatch, capsys):
    # DynamoDB answers with Decimal where a local store answers with int. The
    # carrier type is an artefact of which store replied; the number is not.
    monkeypatch.setenv(trace.ENV, "1")
    trace.emit("kv", "pets", "get", ret={"age": decimal.Decimal("3")})
    assert events(capsys)[0]["ret"] == {"age": 3}


def test_timestamps_are_flattened(monkeypatch, capsys):
    monkeypatch.setenv(trace.ENV, "1")
    trace.emit("kv", "pets", "get", ret={"at": datetime.datetime(2020, 1, 1)})
    assert events(capsys)[0]["ret"] == {"at": "<time>"}


def test_uuids_are_numbered_in_first_seen_order(monkeypatch, capsys):
    monkeypatch.setenv(trace.ENV, "1")
    a = "3f2504e0-4f89-11d3-9a0c-0305e82c3301"
    b = "550e8400-e29b-41d4-a716-446655440000"
    trace.emit("kv", "pets", "put", args={"id": a})
    trace.emit("kv", "pets", "put", args={"id": b})
    trace.emit("kv", "pets", "get", args={"id": a})

    got = [e["args"]["id"] for e in events(capsys)]
    # Same order, same placeholders -- so two runs agree. A run that minted a
    # different *number* of ids still diverges, which is the property kept.
    assert got == ["<uuid:1>", "<uuid:2>", "<uuid:1>"]


def test_transport_metadata_is_dropped(monkeypatch, capsys):
    # boto3 returns the HTTP headers, the server date and a fresh request id
    # beside every answer. Left in, two identical runs would differ on every
    # single event and the comparison would be worthless.
    monkeypatch.setenv(trace.ENV, "1")
    trace.emit("kv", "pets", "get_item", ret={
        "Item": {"id": "1"},
        "ResponseMetadata": {"HTTPStatusCode": 200, "RequestId": "abc"},
        "ConsumedCapacity": {"CapacityUnits": 1.0},
    })
    assert events(capsys)[0]["ret"] == {"Item": {"id": "1"}}


def test_dict_key_order_does_not_matter(monkeypatch, capsys):
    monkeypatch.setenv(trace.ENV, "1")
    trace.emit("kv", "pets", "put", args={"b": 1, "a": 2})
    trace.emit("kv", "pets", "put", args={"a": 2, "b": 1})
    a, b = events(capsys)
    assert a == b


# -------------------------------------------------------------- the file store

def test_the_key_written_to_is_recorded(monkeypatch, capsys, tmp_path):
    # A file store traced as "something was written" would be blind to the one
    # bug most worth catching: writing to the wrong key.
    monkeypatch.setenv(trace.ENV, "1")
    (tmp_path / "pets").mkdir()
    root = trace.wrap(tmp_path, "fs", "docs")
    (root / "pets" / "1.json").write_text("{}")

    (rec,) = events(capsys)
    assert rec["op"] == "write_text"
    assert rec["args"]["path"] == "pets/1.json"
    assert "err" not in rec


def test_two_roots_compare_below_the_root(monkeypatch, capsys, tmp_path):
    # The local root is a directory and the compiled one is an s3:// URL. They
    # must compare equal below the root or every file operation would differ.
    monkeypatch.setenv(trace.ENV, "1")
    assert trace.canon(str(tmp_path / "a" / "b.json"), root=str(tmp_path)) == "a/b.json"
    assert trace.canon("s3://bucket/a/b.json", root="s3://bucket") == "a/b.json"


# ------------------------------------------------------------------- async

def test_an_awaited_result_is_recorded_not_the_coroutine(monkeypatch, capsys):
    monkeypatch.setenv(trace.ENV, "1")

    class AsyncStore:
        async def get(self, key):
            return {"name": "rex"}

    store = trace.wrap(AsyncStore(), "orm", "shopdb")
    assert asyncio.run(store.get("1")) == {"name": "rex"}

    (rec,) = events(capsys)
    # Recording at call time would have captured a coroutine object and told
    # us nothing about what it did.
    assert rec["ret"] == {"name": "rex"}


def test_an_async_failure_is_recorded_and_re_raised(monkeypatch, capsys):
    monkeypatch.setenv(trace.ENV, "1")

    class AsyncStore:
        async def get(self, key):
            raise ValueError("no")

    store = trace.wrap(AsyncStore(), "orm", "shopdb")
    with pytest.raises(ValueError):
        asyncio.run(store.get("1"))
    assert events(capsys)[0]["err"] == "ValueError"


# ------------------------------------------------------------ transparency
#
# Everything below asserts the proxy did NOT change the program.

def test_the_return_value_reaches_the_caller_unchanged(monkeypatch):
    monkeypatch.setenv(trace.ENV, "1")
    store = trace.wrap(Store(), "kv", "pets")
    assert store.put("1", {"name": "rex"}) == {"ok": True}
    assert store.get("1") == {"name": "rex"}


def test_iteration_still_works(monkeypatch):
    monkeypatch.setenv(trace.ENV, "1")
    assert list(trace.wrap([1, 2, 3], "kv", "x")) == [1, 2, 3]


def test_len_indexing_and_membership_still_work(monkeypatch):
    monkeypatch.setenv(trace.ENV, "1")
    proxied = trace.wrap({"a": 1}, "kv", "x")
    assert len(proxied) == 1
    assert proxied["a"] == 1
    assert "a" in proxied


def test_paths_still_read_and_write(monkeypatch, tmp_path):
    monkeypatch.setenv(trace.ENV, "1")
    root = trace.wrap(tmp_path, "fs", "docs")
    (root / "x.txt").write_text("hello")
    assert (root / "x.txt").read_text() == "hello"
    assert (root / "x.txt").exists()
    # And the bytes really landed on disk, not in the proxy.
    assert (tmp_path / "x.txt").read_text() == "hello"


def test_attributes_and_equality_pass_through(monkeypatch):
    monkeypatch.setenv(trace.ENV, "1")
    store = Store()
    store.items["a"] = 1
    proxied = trace.wrap(store, "kv", "x")
    assert proxied.items == {"a": 1}
    assert proxied == store
    assert str(proxied) == str(store)


def test_setting_an_attribute_reaches_the_real_object(monkeypatch):
    monkeypatch.setenv(trace.ENV, "1")
    store = Store()
    proxied = trace.wrap(store, "kv", "x")
    proxied.items = {"b": 2}
    assert store.items == {"b": 2}
