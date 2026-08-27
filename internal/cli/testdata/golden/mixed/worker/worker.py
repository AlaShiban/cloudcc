"""The Python worker, sharing three of the Node unit's four stores.

Each store is declared here the way Python declares things -- a boto3 Table, a
SQLAlchemy engine, a pathlib.Path -- and in api.js the way JavaScript does, with
a DynamoDBClient, a pg Pool and an S3Client. The two units name the same ids, so
each pair resolves to one resource and each unit is handed a client of the kind
its own source asked for.

That is the whole claim this example exists to make. `shopdb` is one RDS
Postgres instance behind an ORM on this side and a connection pool on the other,
and neither file mentions RDS, ElastiCache, S3 or DynamoDB.

Uncompiled this talks to a local Postgres:

    docker run -d -p 5432:5432 -e POSTGRES_USER=ccadmin \\
      -e POSTGRES_DB=shopdb -e POSTGRES_HOST_AUTH_METHOD=trust postgres:16-alpine
"""
# Injected by cloudcc: runtime clients for this program's declared capabilities.
from _cloudcc_runtime import fs as _cloudcc_fs
from _cloudcc_runtime import kv as _cloudcc_kv
from _cloudcc_runtime import orm as _cloudcc_orm


import json
from pathlib import Path

import boto3
from sqlalchemy import create_engine, select
from sqlalchemy.orm import DeclarativeBase, Mapped, Session, mapped_column


None

# The Node unit in api.js declares the same id, which is what makes both units
# resolve to one table. What each language holds is its own AWS client -- a
# boto3 Table here, a DynamoDBClient there -- and what they agree on is the
# item shape, explicitly.
pets = _cloudcc_kv.connect("petsByOwner")


class Base(DeclarativeBase):
    pass


class Sighting(Base):
    """One breed, and how often it has been seen.

    An ordinary declarative model over the same three columns api.js creates
    with raw SQL. Neither side is the schema's owner; they agree on it the same
    way they agree on the DynamoDB item shape.
    """

    __tablename__ = "sightings"

    breed: Mapped[str] = mapped_column(primary_key=True)
    species: Mapped[str] = mapped_column(default="unknown")
    seen: Mapped[int] = mapped_column(default=0)


# The URL is the local one, and it carries no password because the compiled
# form has none either: AWS manages the master credential and the shim splices
# it in from the managed secret.
engine = _cloudcc_orm.connect("shopdb", library="sqlalchemy")

# A pathlib.Path asks for a bucket, and every method a Path has still applies --
# because what comes back is a cloudpathlib.S3Path, not a wrapper of cloudcc's.
# api.js reaches the same bucket through an S3Client.
photos = _cloudcc_fs.connect("petPhotos")


def summarise() -> int:
    """How many pets the shared table holds."""
    return len(pets.scan(ProjectionExpression="id").get("Items", []))


def sightings() -> list[dict]:
    """Every breed the Node unit has recorded, read through the ORM."""
    Base.metadata.create_all(engine)
    with Session(engine) as session:
        return [
            {"breed": row.breed, "species": row.species, "seen": row.seen}
            for row in session.scalars(select(Sighting).order_by(Sighting.breed))
        ]


def record_sighting(breed: str, species: str = "unknown") -> int:
    """Count one sighting, and return the new total.

    The same row api.js writes with an upsert, written here through the ORM.
    """
    Base.metadata.create_all(engine)
    with Session(engine) as session:
        row = session.get(Sighting, breed)
        if row is None:
            row = Sighting(breed=breed, species=species, seen=0)
            session.add(row)
        row.seen += 1
        session.commit()
        return row.seen


def photo(pet_id: str) -> dict | None:
    """Read back a photo record api.js put in the bucket."""
    target = photos / f"{pet_id}.json"
    if not target.exists():
        return None
    return json.loads(target.read_text())


def archive(pet_id: str, note: str) -> str:
    """Write a note beside the photo, from the Python side of the same bucket."""
    target = photos / f"{pet_id}.note.txt"
    target.write_text(note)
    return target.name
