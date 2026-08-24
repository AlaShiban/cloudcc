"""CloudCompiler SDK: compile-time hints for a plain Python application.

Every public function in this module is a *hint*. The compiler reads these
calls statically -- it never imports or executes this package -- and rewrites
them, in a copy of your source, into real cloud clients.

Two rules follow from that:

* Arguments must be literals. ``cc.persist_kv("pets")`` is fine;
  ``cc.persist_kv(name)`` is a compile error with a precise source location,
  because the compiler would have to run your program to know the value.
* Calls belong at module level, where the compiler can see the shape of the
  program. ``execution_unit`` in particular must be a module-level call.

At runtime, outside the compiler, these functions return small local
emulations -- an in-memory dictionary for a KV store, a directory for a
bucket -- so ``uvicorn app:app`` still runs on your laptop with no cloud
account and no credentials. The emulations are deliberately minimal; they
exist so the program runs, not so it behaves identically to AWS.

This package never imports boto3. Cloud access only ever appears in the
``_cc_runtime`` package the compiler injects into the compiled copy.
"""

from __future__ import annotations

from ._emulation import (
    Bucket,
    Gateway,
    KVStore,
    OrmSession,
    Redis,
    Secret,
    Topic,
    local_root,
    reset_local_state,
)

__all__ = [
    "execution_unit",
    "expose",
    "persist_kv",
    "persist_fs",
    "persist_secret",
    "persist_orm",
    "persist_redis",
    "pubsub_topic",
    "config_value",
    "static_unit",
    "embed_assets",
    "Bucket",
    "Gateway",
    "KVStore",
    "OrmSession",
    "Redis",
    "Secret",
    "Topic",
    "local_root",
    "reset_local_state",
    "__version__",
]

__version__ = "0.1.0"

_KV: dict[str, KVStore] = {}
_FS: dict[str, Bucket] = {}
_SECRETS: dict[str, Secret] = {}
_ORM: dict[str, OrmSession] = {}
_REDIS: dict[str, Redis] = {}
_TOPICS: dict[str, Topic] = {}


def execution_unit(id: str, type: str | None = None) -> None:
    """Mark this module as the entrypoint of an execution unit.

    The unit's contents are the transitive local-import closure of this
    module. A program with no ``execution_unit`` call at all is compiled as a
    single unit named ``main``.

    ``type`` is a weak hint ("lambda", "ecs"); ``cc.yaml`` overrides it.
    """
    return None


def expose(app, id: str = "main", target: str = "public") -> Gateway:
    """Expose an ASGI application to the network.

    ``app`` is the application object itself -- the one argument that is an
    expression rather than a literal, because the compiler only needs to know
    *which variable* holds it.
    """
    return Gateway(id=id, target=target, app=app)


def persist_kv(id: str) -> KVStore:
    """A key/value store. Compiles to DynamoDB."""
    return _KV.setdefault(id, KVStore(id))


def persist_fs(id: str) -> Bucket:
    """A file store. Compiles to S3."""
    return _FS.setdefault(id, Bucket(id))


def persist_secret(id: str) -> Secret:
    """A secret. Compiles to Secrets Manager."""
    return _SECRETS.setdefault(id, Secret(id))


def persist_orm(id: str, models: list | None = None) -> OrmSession:
    """A relational database. Compiles to RDS Postgres."""
    return _ORM.setdefault(id, OrmSession(id))


def persist_redis(id: str) -> Redis:
    """A Redis-compatible cache. Compiles to ElastiCache or MemoryDB."""
    return _REDIS.setdefault(id, Redis(id))


def pubsub_topic(id: str) -> Topic:
    """A publish/subscribe topic. Compiles to SNS."""
    return _TOPICS.setdefault(id, Topic(id))


def config_value(id: str, default: str = "", secret: bool = False) -> str:
    """A runtime configuration value, delivered as an environment variable.

    ``secret=True`` makes the compiled project store the value as a Pulumi
    stack secret rather than as plaintext.
    """
    import os

    return os.environ.get(_config_env_name(id), default)


def static_unit(
    id: str,
    static_files: str,
    index_document: str = "index.html",
    shared_files: str | None = None,
) -> None:
    """Serve a bundle of static files from object storage.

    ``static_files`` is claimed out of the source pool before execution units
    are assembled, so those assets never end up inside a compute bundle.
    ``shared_files`` are uploaded too but stay importable by your code.
    """
    return None


def embed_assets(pattern: str) -> str:
    """Claim files matching ``pattern`` so they travel with the execution unit.

    Returns the pattern unchanged, so it can be used inline where a path or
    glob is expected.
    """
    return pattern


def _config_env_name(id: str) -> str:
    """The environment variable a config value is delivered in.

    Kept here rather than inlined so the compiler, the shims and the SDK all
    agree on one spelling.
    """
    return "CC_CONFIG_" + "".join(c.upper() if c.isalnum() else "_" for c in id)
