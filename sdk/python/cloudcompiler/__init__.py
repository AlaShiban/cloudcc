"""CloudCompiler SDK: compile-time hints for a plain Python application.

Every public function here is a *hint*. The compiler reads these calls
statically -- it never imports or executes this package -- and rewrites them,
in a copy of your source, into clients pointed at real cloud resources.

The central idea is that you bring your own client and the compiler wraps it::

    cache = cloudcc.persist(Redis(host="localhost"), id="itemCache")
    db    = cloudcc.persist(create_engine("postgresql://localhost/shop"), id="shopdb")
    docs  = cloudcc.persist(Path("./itemDocs"), id="itemDocs")

``persist`` is type-preserving: it returns exactly what you gave it, so the
type your editor sees is the type you keep. Uncompiled, it *is* the object you
passed -- your program talks to a local Redis, a local Postgres, a local
directory. Compiled, the same expression becomes a client of the same type
pointed at ElastiCache, RDS or S3.

That is the whole point of the design. There is no parallel API to learn and
none for us to keep in step with yours: what you hold is always the library's
own type.

Two rules follow from the hints being read rather than run:

* Arguments must be literals. ``persist(client, id="pets")`` is fine;
  ``persist(client, id=name)`` is a compile error with a precise source
  location, because the compiler would have to run your program to know the
  value.
* Calls belong at module level, where the compiler can see the shape of the
  program. ``execution_unit`` in particular must be a module-level call.

This package supplies no client for a data store. A store is declared by
wrapping the library you already use -- ``redis.Redis``, ``pathlib.Path``,
``boto3.resource("dynamodb").Table(...)`` -- because a class of ours would be a
dialect nobody else speaks, and its methods would have to be kept in step with
the injected runtime's forever. The two things it does supply, a pub/sub topic
and a secret, are not stores: neither has a client to wrap.

This package never imports boto3. Cloud access only ever appears in the
``_cloudcc_runtime`` package the compiler injects into the compiled copy.
"""

from __future__ import annotations

from typing import Any, TypeVar

from ._emulation import (
    Gateway,
    Secret,
    Topic,
    local_root,
    reset_local_state,
)

__all__ = [
    "persist",
    "expose",
    "execution_unit",
    "config_value",
    "static_unit",
    "embed_assets",
    "Topic",
    "Secret",
    "Gateway",
    "local_root",
    "reset_local_state",
    "__version__",
]

__version__ = "0.2.0"

#: The type of whatever client is being persisted. ``persist`` returns it
#: unchanged, so type information flows straight through the call.
T = TypeVar("T")


def persist(client: T, *, id: str, models: list | None = None) -> T:
    """Make a client's data outlive the process.

    Pass the client you already use. The compiler reads its type to decide
    what to provision:

    ============================================  =========================
    what you pass                                 what it becomes
    ============================================  =========================
    ``redis.Redis(...)``                          ElastiCache (or MemoryDB)
    ``sqlalchemy.create_engine("postgresql…")``   RDS Postgres
    ``sqlalchemy.create_engine("mysql…")``        RDS MySQL
    ``pathlib.Path(...)``                         S3
    ``boto3.resource("dynamodb").Table(...)``     DynamoDB
    ``cloudcc.Topic()``                           SNS, SQS or Kinesis
    ``cloudcc.Secret()``                          Secrets Manager
    ============================================  =========================

    The library you reached for supplies the default; ``cloudcc.yaml`` still
    chooses between variants of it, so asking for MemoryDB instead of
    ElastiCache is a configuration change rather than a code change.

    ``id`` names the resource and is required. It is deliberately not taken
    from the variable you assign to: renaming a local would otherwise replace
    a live resource, and losing a database to a rename is not a trade worth
    making for brevity.

    ``models`` is an optional hint for relational clients, listing the tables
    the program expects.

    Returns ``client``, unchanged.
    """
    return client


def expose(app: Any, id: str = "main", target: str = "public") -> Gateway:
    """Expose an application to the network.

    ``app`` is the application object itself -- a FastAPI instance, a Flask
    app, anything with routes. The compiler only needs to know *which
    variable* holds it.
    """
    return Gateway(id=id, target=target, app=app)


def execution_unit(id: str, type: str | None = None) -> None:
    """Mark this module as the entrypoint of an execution unit.

    The unit's contents are the transitive local-import closure of this
    module. A program with no ``execution_unit`` call at all is compiled as a
    single unit named ``main``.

    ``type`` is a weak hint ("lambda", "ecs"); ``cloudcc.yaml`` overrides it.
    """
    return None


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
    return "CLOUDCC_CONFIG_" + "".join(c.upper() if c.isalnum() else "_" for c in id)
