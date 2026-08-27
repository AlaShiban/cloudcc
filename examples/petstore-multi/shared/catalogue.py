"""The relational half, and the cache in front of it -- both asynchronous.

Two more capabilities, declared the same way as everything else: by wrapping the
client the program already uses. A SQLAlchemy engine pointed at Postgres asks
for RDS Postgres; a Redis client asks for ElastiCache. Neither is a class of
cloudcc's, so the models, the session, every query method and every type stub
SQLAlchemy ships still apply -- and so does ``setex``, which nobody had to
remember to wrap.

What is different here is *which* client each one is. ``create_async_engine``
and ``redis.asyncio.Redis`` are separate libraries from their synchronous
namesakes, and their objects are not interchangeable: awaiting a synchronous
``Redis.get`` raises ``TypeError: object bool can't be used in 'await'
expression``. The compiler records which one the source constructed and the shim
hands back one of the same kind, so ``await`` here means the same thing before
and after compiling.

``shared/ledger.py`` is the other half of the pair: the worker's own database,
declared with ``create_engine`` and reached synchronously. One application, one
compiler, one SDK verb -- and two Lambdas whose relational clients have
different call conventions because their source asked for different ones.

Imported by the api unit alone. The worker has no business reading the
catalogue, and because permissions are derived from what a unit bundles, not
importing it is how the worker ends up without them.

Uncompiled this talks to a local Postgres and a local Redis:

    docker run -d -p 5432:5432 -e POSTGRES_USER=ccadmin \\
      -e POSTGRES_DB=petsdb -e POSTGRES_HOST_AUTH_METHOD=trust postgres:16-alpine
    docker run -d -p 6379:6379 redis:7-alpine
"""

import redis.asyncio
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession, create_async_engine
from sqlalchemy.orm import DeclarativeBase, Mapped, mapped_column

import cloudcompiler as cloudcc


class Base(DeclarativeBase):
    pass


class Breed(Base):
    """One breed, and how often it has been seen.

    An ordinary declarative model. `models=` below names it for the compiler,
    which is a hint about what the database will hold rather than a second
    place to define it.
    """

    __tablename__ = "breeds"

    name: Mapped[str] = mapped_column(primary_key=True)
    species: Mapped[str] = mapped_column(default="unknown")
    seen: Mapped[int] = mapped_column(default=0)


# The URL is the local one, and it carries no password because the compiled
# form has none either: AWS manages the master credential and the shim splices
# it in from the managed secret, so nothing sensitive is ever an environment
# variable.
#
# The driver is named, and has to be: uncompiled there is no shim, and
# create_async_engine on a bare postgresql:// URL loads psycopg2 and refuses it
# with "the asyncio extension requires an async driver". Compiled, the URL comes
# from the environment and the shim swaps in the async driver itself -- so the
# `+asyncpg` here is what makes the *uncompiled* program run, not the compiled
# one.
#
# What the driver does not decide is the resource: `postgresql+asyncpg` and
# `postgresql+psycopg2` are the same RDS Postgres, because the scheme is read up
# to the `+`.
engine = cloudcc.persist(
    create_async_engine("postgresql+asyncpg://ccadmin@localhost:5432/petsdb"),
    id="petsdb",
    models=["Breed"],
)

cache = cloudcc.persist(redis.asyncio.Redis(host="localhost", port=6379), id="petCache")

#: How long a cached summary stays fresh. The point of the cache here is to
#: absorb repeated reads of the same pet rather than to be a second copy of the
#: table, so this wants to be short -- but not shorter than the time it takes
#: anything to look. At sixty seconds the load test's own wait for the
#: asynchronous half outlasted every key, and an empty cache is indistinguishable
#: from one nothing ever wrote to.
CACHE_SECONDS = 300

#: Whether the schema has been created in this process. An async engine cannot
#: be used at import -- there is no running loop yet -- so unlike the
#: synchronous version this is done on first use and remembered, which costs one
#: round trip per cold start rather than one per request.
_schema_ready = False


async def ensure_schema() -> None:
    """Create the table if it is not there.

    At import in a real service this would be a migration run at deploy time;
    an example gets to do the simple thing, and `create_all` is idempotent.

    `run_sync` is how SQLAlchemy runs its synchronous DDL machinery on an async
    connection. It is the library's own escape hatch, not cloudcc's.
    """
    global _schema_ready
    if _schema_ready:
        return
    async with engine.begin() as conn:
        await conn.run_sync(Base.metadata.create_all)
    _schema_ready = True


async def record_sighting(breed: str, species: str) -> int:
    """Count one sighting of a breed, and return the new total.

    `expire_on_commit=False` is not a detail. A commit expires every loaded
    attribute, and reading one back refreshes it -- which a synchronous Session
    does with a blocking query and an AsyncSession cannot do at all: the read on
    the last line raises `MissingGreenlet: greenlet_spawn has not been called`.
    It is the sharpest difference between the two libraries, and the reason
    handing a program the wrong one is worse than a compile error.
    """
    await ensure_schema()
    async with AsyncSession(engine, expire_on_commit=False) as session:
        row = await session.get(Breed, breed)
        if row is None:
            row = Breed(name=breed, species=species, seen=0)
            session.add(row)
        row.seen += 1
        await session.commit()
        return row.seen


async def breeds() -> list[dict]:
    await ensure_schema()
    async with AsyncSession(engine) as session:
        rows = await session.scalars(select(Breed).order_by(Breed.name))
        return [
            {"name": row.name, "species": row.species, "seen": row.seen}
            for row in rows
        ]


async def cache_summary(pet_id: str, summary: str) -> None:
    await cache.setex(f"pet:{pet_id}", CACHE_SECONDS, summary)


async def cached_summary(pet_id: str) -> str | None:
    value = await cache.get(f"pet:{pet_id}")
    if value is None:
        return None
    # decode_responses is not set on the client the program constructed, so
    # what comes back is bytes -- which is what it would be locally too.
    return value.decode("utf-8") if isinstance(value, bytes) else value


async def forget(pet_id: str) -> None:
    await cache.delete(f"pet:{pet_id}")
