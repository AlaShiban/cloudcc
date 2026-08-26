"""The typed clients this package supplies.

**No data stores.** Every store has a client library already -- ``redis.Redis``,
``sqlalchemy.create_engine``, ``pathlib.Path``,
``boto3.resource("dynamodb").Table`` -- and ``persist`` wraps those untouched.
A class of our own would have its own method names, and those would have to be
kept in step with the injected runtime's forever; worse, it would be a dialect
nobody else speaks, so code written against it could not be lifted back out.

What is left are the two capabilities that are not stores. A pub/sub topic is a
decision about how messages move, and a secret is a value the environment
holds; neither has a client to wrap, so each gets a class here.

These are real, working local implementations rather than mocks: a topic
dispatches in process, and a secret reads the environment.

Their method signatures are the contract the injected ``_cloudcc_runtime``
clients must match, and a parity test compares the two.
"""

from __future__ import annotations

import asyncio
import inspect
import os
import pathlib
import shutil
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

    **The arguments are the topic's requirements, and the compiler picks the
    service that meets them.** This is the inversion that makes pub/sub
    different from storage: for a store the library picks the capability and
    cloudcc.yaml picks between variants that behave alike, but here the
    variants do not behave alike -- SNS cannot replay, SQS cannot fan out, and
    FIFO everything costs throughput. Choosing by hand means knowing all of
    that. Declaring the requirement means the compiler has to.

    ==========================  ================================================
    argument                    what it decides
    ==========================  ================================================
    ``subscribers``             ``"many"`` fan-out, or ``"one"`` queue
    ``ordering``                ``"none"``, ``"key"``, or ``"total"``
    ``delivery``                ``"at_least_once"`` or ``"exactly_once"``
    ``replay``                  whether a new subscriber can read the history
    ``retention_hours``         how long a message is kept
    ``max_message_kb``          the largest message you will publish
    ==========================  ================================================

    A set of requirements no service can meet is a compile error naming the
    constraint to relax -- ``ordering="total"`` with ``replay=True`` means a
    single-shard stream, which is a throughput ceiling rather than a design.

    The defaults are what a bare ``Topic()`` has always compiled to: fan-out,
    unordered, at-least-once, which is SNS.

    Locally none of it changes anything: the arguments are read by the
    compiler, and in-process dispatch is in order and exactly once whatever
    they say. That is the usual bargain with an emulation -- the code path is
    exercised, the timing is not.
    """

    def __init__(
        self,
        subscribers: str = "many",
        ordering: str = "none",
        delivery: str = "at_least_once",
        replay: bool = False,
        retention_hours: int = 0,
        max_message_kb: int = 256,
    ) -> None:
        self.subscribers_required = subscribers
        self.ordering = ordering
        self.delivery = delivery
        self.replay = replay
        self.retention_hours = retention_hours
        self.max_message_kb = max_message_kb
        self._subscribers: list[Callable[[dict], Any]] = []

    def publish(self, message: dict) -> None:
        """Deliver ``message`` to every subscriber.

        A subscriber that has to call another execution unit is ``async def``,
        because that call is a network round trip once compiled. Running the
        coroutine here rather than discarding it is what keeps the uncompiled
        program behaving like the compiled one -- the alternative is a handler
        that appears to have run, wrote nothing, and warned about a coroutine
        that was never awaited.
        """
        for fn in list(self._subscribers):
            result = fn(message)
            if inspect.iscoroutine(result):
                asyncio.run(result)

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
