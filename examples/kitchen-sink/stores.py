"""Every stateful capability, declared by wrapping the client that provides it.

The type of what you hand to `persist` is what decides the resource. A Redis
client asks for ElastiCache; a SQLAlchemy engine pointed at Postgres asks for
RDS Postgres; a boto3 Table asks for DynamoDB. Uncompiled, these are exactly
the clients you see -- a real local Redis, a real local database, a local
DynamoDB -- and `persist` hands each one straight back.

Note what is not here: a class of cloudcc's own for any of them. Every store
below is the library's own client, which is why the compiled program can use
every method those libraries have rather than the handful someone thought to
wrap.
"""

from pathlib import Path

import boto3
from redis import Redis
from sqlalchemy import create_engine

import cloudcompiler as cloudcc

catalogue = cloudcc.persist(boto3.resource("dynamodb").Table("catalogue"), id="catalogue")
docs = cloudcc.persist(Path("./itemDocs"), id="itemDocs")
signing_key = cloudcc.persist(cloudcc.Secret(), id="signingKey")
db = cloudcc.persist(
    create_engine("postgresql://ccadmin@localhost:5432/shopdb"),
    id="shopdb",
    models=["Item", "Order"],
)
cache = cloudcc.persist(Redis(host="localhost"), id="itemCache")
events = cloudcc.persist(cloudcc.Topic(), id="itemEvents")


# Both units write documents, so the spelling lives here once -- and it is
# pathlib's spelling, because `docs` is a Path locally and a cloudpathlib
# S3Path once compiled. Neither has a `write(name, data)` method; that belonged
# to an SDK class this project deliberately no longer has.
def write_doc(name: str, data: bytes) -> None:
    path = docs / name
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(data)


def count_docs() -> int:
    """How many documents exist. Empty is a count, not an error."""
    if not docs.exists():
        return 0
    return sum(1 for _ in docs.iterdir())
