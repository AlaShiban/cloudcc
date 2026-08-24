"""The typed clients this package supplies.

Most capabilities have a standard client already -- ``redis.Redis``,
``sqlalchemy.create_engine``, ``pathlib.Path`` -- and ``persist`` wraps those
untouched. Three do not: a key/value store, a pub/sub topic and a secret have
no library everyone reaches for. Those get a class here, so that every
capability is declared the same way.

There is deliberately no file-store class. ``pathlib.Path`` already is one, and
the compiled program gets a ``cloudpathlib.S3Path``, which mirrors it. A class
of our own would have its own method names, and those would have to be kept in
step with S3Path's forever -- which is exactly the drift this design removes.

These are real, working local implementations rather than mocks. A key/value
store is a JSON file, because ``persist`` promising persistence and handing
back a dictionary that vanishes on exit would be a poor joke. A topic
dispatches in process.

Their method signatures are the contract the injected ``_cloudcc_runtime``
clients must match, and a parity test compares the two.
"""

from __future__ import annotations

import json
import os
import pathlib
import shutil
import threading
from typing import Any, Callable, Iterable

#: Where the supplied clients keep their local state.
LOCAL_ROOT_ENV = "CLOUDCC_LOCAL_STATE_DIR"
DEFAULT_LOCAL_ROOT = ".cloudcc-local"


def local_root() -> pathlib.Path:
    """The directory the supplied clients write to."""
    return pathlib.Path(os.environ.get(LOCAL_ROOT_ENV, DEFAULT_LOCAL_ROOT))


def reset_local_state() -> None:
    """Delete every supplied client's local state. Intended for tests."""
    root = local_root()
    if root.exists():
        shutil.rmtree(root)


class KVStore:
    """A key/value store keyed by string, holding JSON-shaped values.

    Compiles to DynamoDB. Locally it is a JSON file under the state directory,
    so the data is still there next time the program runs -- which is what the
    verb wrapping it promises.

    The id is supplied by ``persist``, not by the constructor, so the object
    reads as a plain client until it is wrapped.
    """

    def __init__(self, path: str | None = None) -> None:
        self._explicit = path
        self._lock = threading.Lock()

    @property
    def _path(self) -> pathlib.Path:
        if self._explicit is not None:
            return pathlib.Path(self._explicit)
        return local_root() / "kv" / f"{id(self)}.json"

    def _read(self) -> dict:
        path = self._path
        if not path.is_file():
            return {}
        return json.loads(path.read_text() or "{}")

    def _write(self, items: dict) -> None:
        path = self._path
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(items, sort_keys=True))

    def get(self, key: str) -> dict | None:
        """Return the item at ``key``, or None."""
        with self._lock:
            return self._read().get(str(key))

    def put(self, key: str, value: dict) -> None:
        """Store ``value`` at ``key``."""
        with self._lock:
            items = self._read()
            items[str(key)] = value
            self._write(items)

    def delete(self, key: str) -> None:
        """Remove ``key`` if present."""
        with self._lock:
            items = self._read()
            items.pop(str(key), None)
            self._write(items)

    def keys(self) -> list[str]:
        """Every key currently stored, sorted."""
        with self._lock:
            return sorted(self._read())

    def __repr__(self) -> str:
        return "<KVStore (local)>"


class Secret:
    """A single secret value. Compiles to Secrets Manager.

    Locally it reads ``CLOUDCC_SECRET_<ID>`` from the environment, so a test
    can supply one without a cloud account.
    """

    def __init__(self, env: str | None = None) -> None:
        self._env = env
        self._value: str | None = None

    def get(self) -> str:
        """Return the secret's value."""
        if self._value is not None:
            return self._value
        if self._env:
            return os.environ.get(self._env, "")
        return ""

    def set(self, value: str) -> None:
        """Replace the secret's value."""
        self._value = value

    def __repr__(self) -> str:
        return "<Secret (local)>"


class Topic:
    """A publish/subscribe topic. Compiles to SNS.

    Locally it fans out in process, so a publisher and a subscriber in the
    same program behave as they will once they are separate Lambdas.
    """

    def __init__(self) -> None:
        self._subscribers: list[Callable[[dict], Any]] = []

    def publish(self, message: dict) -> None:
        """Deliver ``message`` to every subscriber."""
        for fn in list(self._subscribers):
            fn(message)

    def subscribe(self, fn: Callable[[dict], Any]) -> Callable[[dict], Any]:
        """Register ``fn`` as a subscriber. Usable as a decorator."""
        self._subscribers.append(fn)
        return fn

    def subscribers(self) -> Iterable[Callable[[dict], Any]]:
        """The registered subscribers, in registration order."""
        return tuple(self._subscribers)

    def __repr__(self) -> str:
        return "<Topic (local)>"


class Gateway:
    """An inert handle returned by ``expose``.

    It exists so the call has a value worth binding and so editors can show
    what was exposed; it has no runtime behaviour of its own.
    """

    def __init__(self, id: str, target: str = "public", app: Any = None) -> None:
        self.id = id
        self.target = target
        self.app = app

    def url(self) -> str:
        """The deployed URL, delivered by the compiler as an environment
        variable. Empty when running locally."""
        env = "CLOUDCC_GATEWAY_" + "".join(
            c.upper() if c.isalnum() else "_" for c in self.id
        ) + "_URL"
        return os.environ.get(env, "")

    def __repr__(self) -> str:
        return f"<Gateway {self.id!r} (local)>"
