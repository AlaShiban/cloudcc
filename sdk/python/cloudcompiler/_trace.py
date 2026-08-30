"""Record what a program does at its cloud seams, so two runs can be compared.

The suite's older correctness check compares HTTP responses before and after
compiling. That check is necessary and not sufficient: it sees only what a
route chose to return. A compiled program that writes to the wrong table, drops
a publish on the floor, or never reads the secret it was given can answer every
request byte-identically and pass. The failure mode is not hypothetical -- an
ORM that raised on every write produced the same 500 twice and the suite called
it green, which is why `examples.sh` grew a server-error guard.

A trace closes that gap by recording the *seams themselves*: every call the
program makes through a persisted client, in order, with its arguments and its
result. Two runs that produce the same trace did the same work, not merely
returned the same bytes.

The design constraint that shapes everything here: the runtime hands back the
library's own object -- a boto3 Table, a SQLAlchemy Engine, an S3Path -- and
that is deliberate (see kv.py). A permanent wrapper would undo it. So tracing
is opt-in and off by default: with CLOUDCC_TRACE unset, `wrap` returns the
client unchanged and this module costs one environment lookup. Only when it is
set does a recording proxy appear, and the proxy forwards everything.

Both halves of the comparison record through *this same code*. The compiled
copy gets it injected; the SDK vendors it verbatim for the uncompiled run, with
a test pinning the two byte-identical. That matters more than it looks: if the
observer differed between halves, a difference in the trace would not tell you
whether the program or the instrument had changed.

Where the events go
-------------------
Stderr, tagged with a marker, because that is the only channel both halves
share. A local process has a filesystem; a Lambda does not, and its stderr is
already shipped to CloudWatch. Setting CLOUDCC_TRACE to a path additionally
writes there, which is the convenient thing locally and useless in Lambda.

What is normalised, and what that costs
---------------------------------------
Every normalisation removes a class of difference this check could otherwise
catch, so each one below is a deliberate trade, not a tidy-up:

* **Physical resource names are never recorded.** The logical id from
  `persist(id=...)` is, and it is identical in both halves by construction.
  This is the one free normalisation -- `pets` against `petstore-pets` is a
  difference that *must* exist, and recording the logical name means the check
  still fails if a unit reaches the wrong store, because the id would differ.
* **Timestamps and durations become "<time>".** They always differ. The cost is
  that a program which writes the wrong *kind* of timestamp is invisible here.
* **UUID-shaped values become "<uuid:N>", numbered in first-seen order.** Two
  runs that generate ids in the same order agree; one that generates a
  different *number* of them still diverges, which is the property worth
  keeping.
* **Path-like values are recorded relative to the persisted root**, so a local
  directory and an s3:// URL compare equal below the root. A write to the wrong
  key still differs.
* **Transport metadata is dropped** -- see `_DROP_KEYS`. An AWS SDK returns the
  HTTP headers, the server's date, its request id and the capacity it consumed
  alongside the answer. None of that is the program's behaviour, all of it
  differs on every call, and left in it would drown the events that matter.

Nothing else is normalised. Values, orderings and error types are compared as
they are, because that is the point.
"""

import base64
import datetime
import decimal
import json
import os
import re
import sys
import threading

#: Set to "1"/"stderr" for stderr, or to a path to also append there.
ENV = "CLOUDCC_TRACE"

#: Prefix on every emitted line. Chosen to survive CloudWatch, interleaved
#: application logging, and a human reading the stream, and to be greppable
#: without matching itself in this file's documentation.
MARKER = "##cloudcc-trace##"

_MISSING = object()
_lock = threading.Lock()
_sink = None
_sink_ready = False
_ids = {}

#: Keys whose values describe the HTTP call rather than the program. Dropped
#: wherever they appear, at any depth.
_DROP_KEYS = frozenset(("ResponseMetadata", "ConsumedCapacity"))

_UUID_RE = re.compile(
    r"\A[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-"
    r"[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\Z"
)


def enabled():
    """Whether tracing is on. Off is the default and costs one env lookup."""
    return bool(os.environ.get(ENV))


def _open_sink():
    """Resolve CLOUDCC_TRACE to an extra file sink, once."""
    global _sink, _sink_ready
    if _sink_ready:
        return _sink
    _sink_ready = True
    value = os.environ.get(ENV, "")
    if value and value not in ("1", "stderr", "true", "yes"):
        try:
            _sink = open(value, "a", encoding="utf-8")
        except OSError as exc:
            # Falling back silently would leave someone waiting for a file that
            # never appears, so say so once and keep tracing to stderr.
            print(
                "cloudcc trace: cannot write %s (%s); tracing to stderr only" % (value, exc),
                file=sys.stderr,
            )
            _sink = None
    return _sink


def emit(kind, id, op, args=_MISSING, ret=_MISSING, err=_MISSING, root=None):
    """Record one seam event.

    `kind` is the capability (kv, orm, fs, redis, pubsub, secret, rpc, config),
    `id` the logical resource name, `op` the operation. Anything omitted is
    left out of the record rather than written as null, so the line stays
    readable and a plain diff points at what actually changed.
    """
    if not enabled():
        return
    rec = {"kind": kind, "id": id, "op": op}
    if args is not _MISSING:
        rec["args"] = canon(args, root)
    if ret is not _MISSING:
        rec["ret"] = canon(ret, root)
    if err is not _MISSING:
        rec["err"] = err
    line = MARKER + " " + json.dumps(rec, sort_keys=True, separators=(",", ":"))
    with _lock:
        print(line, file=sys.stderr, flush=True)
        sink = _open_sink()
        if sink is not None:
            sink.write(line + "\n")
            sink.flush()


def canon(value, root=None):
    """Reduce a value to something two runs can be compared on.

    See the module docstring for what is normalised and what that costs.
    """
    return _canon(value, root, 0)


def _canon(v, root, depth):
    if depth > 12:
        # Deep structures are almost always a client object that slipped
        # through rather than data worth comparing.
        return "<deep>"

    if v is None or isinstance(v, bool):
        return v
    if isinstance(v, int):
        return v
    if isinstance(v, float):
        # -0.0 and 0.0 compare equal but serialise differently.
        return v + 0.0
    if isinstance(v, decimal.Decimal):
        # DynamoDB hands back Decimal where the local half may have int/float.
        # Comparing the *number* is the intent; the carrier type is an artefact
        # of which store answered.
        as_int = int(v)
        return as_int if v == as_int else float(v)
    if isinstance(v, str):
        return _canon_str(v, root)
    if isinstance(v, (bytes, bytearray)):
        # Short payloads compare by content; long ones by length, because a
        # bundled binary would otherwise dominate the trace.
        b = bytes(v)
        if len(b) <= 64:
            return "<b64:" + base64.b64encode(b).decode("ascii") + ">"
        return "<bytes:%d>" % len(b)
    if isinstance(v, (datetime.datetime, datetime.date, datetime.time)):
        return "<time>"
    if isinstance(v, dict):
        return {
            str(k): _canon(v[k], root, depth + 1)
            for k in sorted(v, key=str)
            if str(k) not in _DROP_KEYS
        }
    if isinstance(v, (list, tuple)):
        return [_canon(x, root, depth + 1) for x in v]
    if isinstance(v, (set, frozenset)):
        # A set has no order to preserve, so give it one deterministically.
        return sorted((_canon(x, root, depth + 1) for x in v), key=repr)

    if isinstance(v, _Proxy):
        return _canon(v._cc_obj, root, depth + 1)

    # SQLAlchemy Row and anything else exposing the mapping protocol.
    mapping = getattr(v, "_mapping", None)
    if mapping is not None:
        try:
            return _canon(dict(mapping), root, depth + 1)
        except Exception:
            pass
    if hasattr(v, "_asdict"):
        try:
            return _canon(v._asdict(), root, depth + 1)
        except Exception:
            pass

    # Path-like: pathlib.Path locally, cloudpathlib.S3Path compiled.
    if hasattr(v, "__fspath__") or type(v).__name__ in ("S3Path", "CloudPath", "PosixPath", "WindowsPath", "Path"):
        return _relpath(str(v), root)

    return "<%s>" % type(v).__name__


def _canon_str(s, root):
    if _UUID_RE.match(s):
        with _lock:
            if s not in _ids:
                _ids[s] = "<uuid:%d>" % (len(_ids) + 1)
            return _ids[s]
    if root:
        return _relpath(s, root)
    return s


def _relpath(s, root):
    """Strip a persisted root, so two halves compare below it."""
    if not root:
        return s
    if s.startswith(root):
        rest = s[len(root):]
        return rest.lstrip("/") or "."
    return s


def reset():
    """Forget generated-id numbering. For tests that run several cases."""
    with _lock:
        _ids.clear()


# --------------------------------------------------------------------------
# The proxy
#
# Transparent by construction: every attribute is fetched from the target and
# every call forwarded, so the object a program holds behaves as the library's
# own. It records on the way through.
#
# Dunder methods are looked up on the type, not the instance, so __getattr__
# never sees them and each one that matters is written out below. The list is
# what the store clients in this repo actually use -- context managers for
# SQLAlchemy connections and DynamoDB batch writers, `/` and iteration for
# paths, indexing for responses.
# --------------------------------------------------------------------------


def wrap(client, kind, id, root=None):
    """Return `client` unchanged, or a recording proxy when tracing is on."""
    if not enabled():
        return client
    if isinstance(client, _Proxy):
        return client
    if root is None:
        root = _root_of(client)
    return _Proxy(client, kind, id, (), root)


def _root_of(client):
    """The string a path-like client is rooted at, so keys compare relatively."""
    if hasattr(client, "__fspath__") or type(client).__name__ in (
        "S3Path", "CloudPath", "PosixPath", "WindowsPath", "Path"
    ):
        return str(client)
    return None


def _is_data(v):
    """Whether a returned value is data to record rather than an object to follow."""
    return v is None or isinstance(
        v, (bool, int, float, str, bytes, bytearray, dict, list, tuple, set,
            frozenset, decimal.Decimal, datetime.datetime, datetime.date,
            datetime.time)
    )


class _Proxy(object):
    """Forwards everything to `_cc_obj`, recording calls made through it."""

    __slots__ = ("_cc_obj", "_cc_kind", "_cc_id", "_cc_path", "_cc_root")

    def __init__(self, obj, kind, id, path, root):
        object.__setattr__(self, "_cc_obj", obj)
        object.__setattr__(self, "_cc_kind", kind)
        object.__setattr__(self, "_cc_id", id)
        object.__setattr__(self, "_cc_path", path)
        object.__setattr__(self, "_cc_root", root)

    # -- attribute access --------------------------------------------------

    def __getattr__(self, name):
        v = getattr(self._cc_obj, name)
        if callable(v) and not isinstance(v, type):
            return _Bound(v, self._cc_kind, self._cc_id,
                          self._cc_path + (name,), self._cc_root, self._cc_obj)
        return v

    def __setattr__(self, name, value):
        setattr(self._cc_obj, name, value)

    def __delattr__(self, name):
        delattr(self._cc_obj, name)

    def __dir__(self):
        return dir(self._cc_obj)

    # -- protocols ---------------------------------------------------------

    def __call__(self, *a, **kw):
        return _Bound(self._cc_obj, self._cc_kind, self._cc_id,
                      self._cc_path or ("__call__",), self._cc_root)(*a, **kw)

    def __enter__(self):
        return _follow(self._cc_obj.__enter__(), self._cc_kind, self._cc_id,
                       self._cc_path, self._cc_root)

    def __exit__(self, *a):
        return self._cc_obj.__exit__(*a)

    async def __aenter__(self):
        return _follow(await self._cc_obj.__aenter__(), self._cc_kind,
                       self._cc_id, self._cc_path, self._cc_root)

    async def __aexit__(self, *a):
        return await self._cc_obj.__aexit__(*a)

    def __iter__(self):
        for item in self._cc_obj:
            yield _follow(item, self._cc_kind, self._cc_id, self._cc_path, self._cc_root)

    def __len__(self):
        return len(self._cc_obj)

    def __getitem__(self, k):
        return _follow(self._cc_obj[k], self._cc_kind, self._cc_id,
                       self._cc_path, self._cc_root)

    def __setitem__(self, k, v):
        self._cc_obj[k] = v

    def __contains__(self, k):
        return k in self._cc_obj

    def __truediv__(self, other):
        # pathlib's `/`. Not recorded -- building a path is not a seam call --
        # but the result stays wrapped so the read or write at the end of the
        # chain is, and carries the same root so the key compares relatively.
        return _follow(self._cc_obj / other, self._cc_kind, self._cc_id,
                       self._cc_path, self._cc_root)

    def __fspath__(self):
        return self._cc_obj.__fspath__()

    def __str__(self):
        return str(self._cc_obj)

    def __repr__(self):
        return repr(self._cc_obj)

    def __eq__(self, other):
        if isinstance(other, _Proxy):
            other = other._cc_obj
        return self._cc_obj == other

    def __ne__(self, other):
        return not self.__eq__(other)

    def __hash__(self):
        return hash(self._cc_obj)

    def __bool__(self):
        return bool(self._cc_obj)


def _follow(v, kind, id, path, root):
    """Keep tracing through a returned object; leave plain data alone."""
    if _is_data(v):
        return v
    return _Proxy(v, kind, id, path, root)


class _Bound(object):
    """A callable pulled off a proxied object, recorded when invoked."""

    __slots__ = ("_fn", "_kind", "_id", "_path", "_root", "_owner")

    def __init__(self, fn, kind, id, path, root, owner=None):
        self._fn = fn
        self._kind = kind
        self._id = id
        self._path = path
        self._root = root
        self._owner = owner

    def __call__(self, *a, **kw):
        op = ".".join(self._path)
        args = self._args(a, kw)
        try:
            result = self._fn(*a, **kw)
        except Exception as exc:
            emit(self._kind, self._id, op, args=args,
                 err=type(exc).__name__, root=self._root)
            raise

        # An async client returns a coroutine here and does the work later.
        # Recording now would record the coroutine object and nothing about
        # what it did, so the await is what gets recorded.
        if _is_awaitable(result):
            return _recorded_await(result, self._kind, self._id, op, args, self._root)

        emit(self._kind, self._id, op, args=args, ret=result, root=self._root)
        return _follow(result, self._kind, self._id, self._path, self._root)

    def _args(self, a, kw):
        out = {}
        if a:
            out["a"] = list(a)
        if kw:
            out["kw"] = kw
        # The relative path a chained client arrived at is part of what the
        # call means: `(root / "pets" / "1.json").write_text(x)` and
        # `(root / "2.json").write_text(x)` are different writes with identical
        # arguments. Without this the file store would be traced as "something
        # was written", which is exactly the blindness this module exists to
        # remove.
        if self._root and self._owner is not None:
            where = _relpath(str(self._owner), self._root)
            if where != ".":
                out["path"] = where
        return out

    def __repr__(self):
        return "<traced %s>" % ".".join(self._path)


def _is_awaitable(v):
    return hasattr(v, "__await__")


async def _recorded_await(awaitable, kind, id, op, args, root):
    try:
        result = await awaitable
    except Exception as exc:
        emit(kind, id, op, args=args, err=type(exc).__name__, root=root)
        raise
    emit(kind, id, op, args=args, ret=result, root=root)
    return result
