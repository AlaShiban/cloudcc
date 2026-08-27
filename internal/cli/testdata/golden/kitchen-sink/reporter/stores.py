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
# Injected by cloudcc: runtime clients for this program's declared capabilities.
from _cloudcc_runtime import fs as _cloudcc_fs
from _cloudcc_runtime import kv as _cloudcc_kv
from _cloudcc_runtime import orm as _cloudcc_orm
from _cloudcc_runtime import pubsub as _cloudcc_pubsub
from _cloudcc_runtime import redis_ as _cloudcc_redis
from _cloudcc_runtime import secret as _cloudcc_secret


from pathlib import Path

import boto3
from redis import Redis
from sqlalchemy import create_engine


catalogue = _cloudcc_kv.connect("catalogue")
docs = _cloudcc_fs.connect("itemDocs")
signing_key = _cloudcc_secret.connect("signingKey")
db = _cloudcc_orm.connect("shopdb", library="sqlalchemy")
cache = _cloudcc_redis.connect("itemCache", library="redis-py")
events = _cloudcc_pubsub.connect("itemEvents")


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
