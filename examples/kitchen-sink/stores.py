"""Every stateful capability, declared by wrapping the client that provides it.

The type of what you hand to `persist` is what decides the resource. A Redis
client asks for ElastiCache; a SQLAlchemy engine pointed at Postgres asks for
RDS Postgres. Uncompiled, these are exactly the clients you see -- a real local
Redis, a real local database -- and `persist` hands each one straight back.
"""

from pathlib import Path

from redis import Redis
from sqlalchemy import create_engine

import cloudcompiler as cloudcc

catalogue = cloudcc.persist(cloudcc.KVStore(), id="catalogue")
docs = cloudcc.persist(Path("./itemDocs"), id="itemDocs")
signing_key = cloudcc.persist(cloudcc.Secret(), id="signingKey")
db = cloudcc.persist(
    create_engine("postgresql://localhost/shop"),
    id="shopdb",
    models=["Item", "Order"],
)
cache = cloudcc.persist(Redis(host="localhost"), id="itemCache")
events = cloudcc.persist(cloudcc.Topic(), id="itemEvents")
