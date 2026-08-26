"""The relational half, and the cache in front of it.

Two more capabilities, declared the same way as everything else: by wrapping the
client the program already uses. A SQLAlchemy engine pointed at Postgres asks
for RDS Postgres; a ``redis.Redis`` asks for ElastiCache. Neither is a class of
cloudcc's, so the models, the session, every query method and every type stub
SQLAlchemy ships still apply -- and so does ``setex``, which nobody had to
remember to wrap.

Imported by the api unit alone. The worker has no business reading the
catalogue, and because permissions are derived from what a unit bundles, not
importing it is how the worker ends up without them.

Uncompiled this talks to a local Postgres and a local Redis:

    docker run -d -p 5432:5432 -e POSTGRES_USER=ccadmin \\
      -e POSTGRES_DB=petsdb -e POSTGRES_HOST_AUTH_METHOD=trust postgres:16-alpine
    docker run -d -p 6379:6379 redis:7-alpine
"""
# Injected by cloudcc: runtime clients for this program's declared capabilities.
from _cloudcc_runtime import orm as _cloudcc_orm
from _cloudcc_runtime import redis_ as _cloudcc_redis


from redis import Redis
from sqlalchemy import create_engine, select
from sqlalchemy.orm import DeclarativeBase, Mapped, Session, mapped_column



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
engine = _cloudcc_orm.connect("petsdb", library="sqlalchemy")

cache = _cloudcc_redis.connect("petCache")

#: How long a cached summary stays fresh. The point of the cache here is to
#: absorb repeated reads of the same pet rather than to be a second copy of the
#: table, so this wants to be short -- but not shorter than the time it takes
#: anything to look. At sixty seconds the load test's own wait for the
#: asynchronous half outlasted every key, and an empty cache is indistinguishable
#: from one nothing ever wrote to.
CACHE_SECONDS = 300


def ensure_schema() -> None:
    """Create the table if it is not there.

    At import in a real service this would be a migration run at deploy time;
    an example gets to do the simple thing, and `create_all` is idempotent. It
    costs one round trip on a cold start, which is the honest price of not
    having a migration step in a twenty-line example.
    """
    Base.metadata.create_all(engine)


def record_sighting(breed: str, species: str) -> int:
    """Count one sighting of a breed, and return the new total."""
    with Session(engine) as session:
        row = session.get(Breed, breed)
        if row is None:
            row = Breed(name=breed, species=species, seen=0)
            session.add(row)
        row.seen += 1
        session.commit()
        return row.seen


def breeds() -> list[dict]:
    with Session(engine) as session:
        return [
            {"name": row.name, "species": row.species, "seen": row.seen}
            for row in session.scalars(select(Breed).order_by(Breed.name))
        ]


def cache_summary(pet_id: str, summary: str) -> None:
    cache.setex(f"pet:{pet_id}", CACHE_SECONDS, summary)


def cached_summary(pet_id: str) -> str | None:
    value = cache.get(f"pet:{pet_id}")
    if value is None:
        return None
    # decode_responses is not set on the client the program constructed, so
    # what comes back is bytes -- which is what it would be locally too.
    return value.decode("utf-8") if isinstance(value, bytes) else value


def forget(pet_id: str) -> None:
    cache.delete(f"pet:{pet_id}")
