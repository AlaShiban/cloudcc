"""Local emulations behind the SDK hints.

These exist for one reason: so that a program written against the SDK still
runs with ``uvicorn app:app`` on a laptop, with no cloud account. They are
deliberately small. A KV store is a dictionary; a bucket is a directory.

Their method signatures are the contract that the injected ``_cloudcc_runtime``
clients must match exactly -- a parity test in the compiler's test suite
compares the two, because two implementations of one API drift otherwise.
"""

from __future__ import annotations

import os
import pathlib
import shutil
import threading
from typing import Any, Callable, Iterable

#: Where directory-backed emulations keep their state.
LOCAL_ROOT_ENV = "CLOUDCC_LOCAL_STATE_DIR"
DEFAULT_LOCAL_ROOT = ".cloudcc-local"


def local_root() -> pathlib.Path:
    """The directory local emulations write to."""
    return pathlib.Path(os.environ.get(LOCAL_ROOT_ENV, DEFAULT_LOCAL_ROOT))


def reset_local_state() -> None:
    """Delete every local emulation's state. Intended for tests."""
    root = local_root()
    if root.exists():
        shutil.rmtree(root)
    for registry in _REGISTRIES:
        registry.clear()


_REGISTRIES: list[dict] = []


class KVStore:
    """A key/value store keyed by string, holding JSON-shaped values."""

    def __init__(self, id: str) -> None:
        self.id = id
        self._items: dict[str, dict] = {}
        self._lock = threading.Lock()

    def get(self, key: str) -> dict | None:
        """Return the item at ``key``, or None."""
        with self._lock:
            item = self._items.get(str(key))
            return dict(item) if item is not None else None

    def put(self, key: str, value: dict) -> None:
        """Store ``value`` at ``key``."""
        with self._lock:
            self._items[str(key)] = dict(value)

    def delete(self, key: str) -> None:
        """Remove ``key`` if present."""
        with self._lock:
            self._items.pop(str(key), None)

    def keys(self) -> list[str]:
        """Every key currently stored, sorted."""
        with self._lock:
            return sorted(self._items)

    def __repr__(self) -> str:
        return f"<KVStore {self.id!r} (local)>"


class Bucket:
    """A file store backed by a local directory."""

    def __init__(self, id: str) -> None:
        self.id = id

    def _path(self, key: str) -> pathlib.Path:
        safe = str(key).lstrip("/")
        return local_root() / "fs" / self.id / safe

    def read(self, key: str) -> bytes:
        """Return the bytes stored at ``key``, raising FileNotFoundError."""
        return self._path(key).read_bytes()

    def write(self, key: str, data: bytes) -> None:
        """Store ``data`` at ``key``."""
        path = self._path(key)
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(data if isinstance(data, bytes) else str(data).encode("utf-8"))

    def delete(self, key: str) -> None:
        """Remove ``key`` if present."""
        self._path(key).unlink(missing_ok=True)

    def exists(self, key: str) -> bool:
        """Whether ``key`` is present."""
        return self._path(key).is_file()

    def list(self, prefix: str = "") -> list[str]:
        """Every key under ``prefix``, sorted."""
        base = local_root() / "fs" / self.id
        if not base.is_dir():
            return []
        out = []
        for path in base.rglob("*"):
            if path.is_file():
                rel = path.relative_to(base).as_posix()
                if rel.startswith(prefix):
                    out.append(rel)
        return sorted(out)

    def __repr__(self) -> str:
        return f"<Bucket {self.id!r} (local)>"


class Secret:
    """A single secret value."""

    def __init__(self, id: str) -> None:
        self.id = id
        self._value: str | None = None

    def get(self) -> str:
        """Return the secret's value.

        Locally this reads ``CLOUDCC_SECRET_<ID>`` from the environment so tests can
        provide one without a cloud account.
        """
        if self._value is not None:
            return self._value
        env = "CLOUDCC_SECRET_" + "".join(c.upper() if c.isalnum() else "_" for c in self.id)
        return os.environ.get(env, "")

    def set(self, value: str) -> None:
        """Replace the secret's value."""
        self._value = value

    def __repr__(self) -> str:
        return f"<Secret {self.id!r} (local)>"


class OrmSession:
    """A relational database handle.

    The emulation offers the connection URL rather than a session object, so
    the same call works with SQLAlchemy, psycopg, or anything else. Locally the
    URL points at a SQLite file.
    """

    def __init__(self, id: str) -> None:
        self.id = id

    def url(self) -> str:
        """The database connection URL."""
        root = local_root() / "orm"
        root.mkdir(parents=True, exist_ok=True)
        return f"sqlite:///{(root / (self.id + '.db')).as_posix()}"

    def __repr__(self) -> str:
        return f"<OrmSession {self.id!r} (local)>"


class Redis:
    """A Redis-compatible cache."""

    def __init__(self, id: str) -> None:
        self.id = id
        self._items: dict[str, str] = {}
        self._lock = threading.Lock()

    def get(self, key: str) -> str | None:
        """Return the value at ``key``, or None."""
        with self._lock:
            return self._items.get(str(key))

    def set(self, key: str, value: str, ex: int | None = None) -> None:
        """Store ``value`` at ``key``, optionally expiring after ``ex`` seconds.

        The local emulation ignores ``ex``: nothing here is long-lived enough
        for expiry to be observable.
        """
        with self._lock:
            self._items[str(key)] = str(value)

    def delete(self, key: str) -> None:
        """Remove ``key`` if present."""
        with self._lock:
            self._items.pop(str(key), None)

    def incr(self, key: str, amount: int = 1) -> int:
        """Increment ``key`` and return the new value."""
        with self._lock:
            value = int(self._items.get(str(key), "0")) + amount
            self._items[str(key)] = str(value)
            return value

    def __repr__(self) -> str:
        return f"<Redis {self.id!r} (local)>"


class Topic:
    """A publish/subscribe topic with in-process fan-out."""

    def __init__(self, id: str) -> None:
        self.id = id
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
        return f"<Topic {self.id!r} (local)>"


class Gateway:
    """An inert handle returned by ``expose``.

    It exists so the call has a value worth binding and so IDEs can show what
    was exposed; it has no runtime behaviour of its own.
    """

    def __init__(self, id: str, target: str = "public", app: Any = None) -> None:
        self.id = id
        self.target = target
        self.app = app

    def url(self) -> str:
        """The deployed URL, delivered by the compiler as an environment
        variable. Empty when running locally."""
        env = "CLOUDCC_GATEWAY_" + "".join(c.upper() if c.isalnum() else "_" for c in self.id) + "_URL"
        return os.environ.get(env, "")

    def __repr__(self) -> str:
        return f"<Gateway {self.id!r} (local)>"
