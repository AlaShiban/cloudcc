"""Stores that are not relational: Redis, Mongo, DynamoDB.

The DynamoDB entry is the one that changed the SDK. There used to be a
`cloudcc.KVStore()` class here -- an object this project supplied because the
Python ecosystem has no single key/value client everyone reaches for. It is
gone, and the rule that replaced it is:

    **cloudcc does not supply objects for data stores.** A store is declared by
    wrapping the client library you would have used anyway.

The reason is the one the whole `persist` design rests on. A supplied class has
its own method names, and those have to stay in step with the shim's forever;
the parity test that compares them exists because they drift. Worse, the class
is a dialect nobody else speaks -- code written against it cannot be lifted out
of cloudcc, and every example of how to use DynamoDB on the internet is written
against boto3.

The cost is honest and worth stating: an uncompiled run now needs a real
DynamoDB endpoint, because a real boto3 client is what it holds. That is
already true of every other store here -- redis-py wants a Redis, SQLAlchemy
wants a Postgres -- so the KV store has stopped being the exception rather than
started being awkward.
"""

from pymongo import MongoClient
from redis import Redis

import boto3

import cloudcompiler as cloudcc

# ---------------------------------------------------------------------------
# 1. redis-py
# ---------------------------------------------------------------------------
#
# shim: `_cloudcc_runtime.redis.connect(id, library="redis-py")` returns a
# `redis.Redis` pointed at the provisioned cluster, with TLS on and the auth
# token from the managed secret. redis-py connects lazily, so nothing happens
# at import.
#
# `cloudcc.yaml` chooses between ElastiCache and MemoryDB. The library cannot
# make that choice, because the API is identical either way -- it is a
# durability decision, and it belongs in configuration.
cache = cloudcc.persist(Redis(host="localhost", port=6379), id="itemCache")


# ---------------------------------------------------------------------------
# 2. boto3 DynamoDB -- the key/value store
# ---------------------------------------------------------------------------
#
# `boto3.resource("dynamodb").Table(name)` is the right object to wrap for the
# same reason `pathlib.Path` is: it is bound to one store, it is lazy, and it
# is what the program would have held anyway.
#
# The name in the call is the *local* table name, and the physical name in the
# cloud is chosen by the compiler -- which is why `id` is a separate argument.
# Uncompiled, the program talks to a table called "orders" on whatever endpoint
# AWS_ENDPOINT_URL names; compiled, it talks to "mega-app-orders-a1b2c3", and
# neither the call sites nor the queries change.
#
# shim: `_cloudcc_runtime.kv.connect(id)` returns a real
# `boto3.resource("dynamodb").Table` bound to the provisioned table, using the
# unit's own role. There is no cloudcc class in the return path at all, which
# is the point: `put_item`, `query`, `batch_writer` and the paginators all work
# because they *are* boto3's.
#
# The compiler grants this unit access to exactly this table -- the thing a
# hand-written policy always gets slightly wrong, and always in the permissive
# direction.
orders = cloudcc.persist(
    boto3.resource("dynamodb").Table("orders"),
    id="orders",
)


# ---------------------------------------------------------------------------
# 3. PyMongo -- proposed
# ---------------------------------------------------------------------------
#
# `MongoClient` is lazy by design: it does not connect in its constructor, it
# starts a background monitor and resolves on first operation. Ideal.
#
# shim: returns a MongoClient pointed at the provisioned DocumentDB cluster,
# with the CA bundle configured and credentials from the managed secret.
#
# open question: DocumentDB is Mongo-compatible, not Mongo. Aggregation stages
# and change streams differ, so a program that compiles cleanly can still fail
# at runtime on a feature the local Mongo supported. This is the strongest
# argument in the whole example for the differential harness: uncompiled runs
# against real Mongo and compiled runs against DocumentDB, and a behavioural
# difference shows up as a mismatched response rather than a support ticket.
# `cloudcc.yaml` should also allow `type: atlas` for people who want the real
# thing.
documents = cloudcc.persist(MongoClient("mongodb://localhost:27017"), id="itemDocs")


# ---------------------------------------------------------------------------
# Deliberately absent: Cassandra and ClickHouse
# ---------------------------------------------------------------------------
#
# Both were written out here and both are cut. Neither is a bad library; the
# problem is what they would commit cloudcc to.
#
# Amazon Keyspaces is Cassandra-compatible in the way DocumentDB is
# Mongo-compatible, and the gaps are in DDL and in the parts of CQL that
# programs actually use -- so "supported" would mean a program that compiles
# and then fails on a query the local Cassandra answered. ClickHouse has no
# managed AWS service at all: the only answers are an always-on cluster
# somebody has to operate, or an account with another vendor. Neither is a
# default a compiler should pick on an author's behalf, and offering the choice
# is offering to maintain both.
#
# A capability whose local emulation and deployed resource differ in ways the
# differential harness cannot catch is worse than no capability, because it
# fails late and looks like the program's fault.
