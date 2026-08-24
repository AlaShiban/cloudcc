"""Every stateful capability, declared by wrapping the client that provides it.

The type of what you hand to `persist` is what decides the resource. A Redis
client asks for ElastiCache; a SQLAlchemy engine pointed at Postgres asks for
RDS Postgres. Uncompiled, these are exactly the clients you see -- a real local
Redis, a real local database -- and `persist` hands each one straight back.
"""
# Injected by cloudcc: runtime clients for this program's declared capabilities.
from _cloudcc_runtime import fs as _cloudcc_fs
from _cloudcc_runtime import kv as _cloudcc_kv
from _cloudcc_runtime import orm as _cloudcc_orm
from _cloudcc_runtime import pubsub as _cloudcc_pubsub
from _cloudcc_runtime import redis_ as _cloudcc_redis
from _cloudcc_runtime import secret as _cloudcc_secret


from pathlib import Path

from redis import Redis
from sqlalchemy import create_engine


catalogue = _cloudcc_kv.connect("catalogue")
docs = _cloudcc_fs.connect("itemDocs")
signing_key = _cloudcc_secret.connect("signingKey")
db = _cloudcc_orm.connect("shopdb")
cache = _cloudcc_redis.connect("itemCache")
events = _cloudcc_pubsub.connect("itemEvents")
