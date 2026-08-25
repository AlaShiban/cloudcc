"""Everything else that stores data: Redis, Mongo, DynamoDB, Cassandra,
ClickHouse.

Two of these are the interesting cases.

``boto3`` is interesting because it is both a client library a program uses and
the library the shims themselves are built from. A program that reaches for a
DynamoDB Table directly is not doing anything wrong -- it is just declaring the
same resource in a lower-level way, and it should get the same table.

``clickhouse-driver`` is interesting because there is no managed AWS service
behind it, which is the case the compiler must handle by *refusing* rather than
by picking something approximate.
"""

from cassandra.cluster import Cluster
from clickhouse_driver import Client as ClickHouseClient
from pymongo import MongoClient
from redis import Redis

import boto3

import cloudcompiler as cloudcc

# ---------------------------------------------------------------------------
# 1. redis-py -- supported today
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
# 2. PyMongo -- proposed
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
# at runtime on a feature the emulation supported. This is the strongest
# argument in the whole example for the differential harness: uncompiled runs
# against real Mongo and compiled runs against DocumentDB, and any behavioural
# difference shows up as a mismatched response rather than as a support ticket.
# `cloudcc.yaml` should also allow `type: atlas` for people who want the real
# thing.
documents = cloudcc.persist(MongoClient("mongodb://localhost:27017"), id="itemDocs")


# ---------------------------------------------------------------------------
# 3. boto3 DynamoDB -- proposed
# ---------------------------------------------------------------------------
#
# The same table `cloudcc.KVStore()` would have provisioned, declared through
# the AWS SDK instead. Note that the string here is the *local* table name, and
# the physical name in the cloud is chosen by the compiler -- which is the whole
# point of the `id`, and the reason the two are separate arguments.
#
# shim: returns a `boto3.resource("dynamodb").Table` bound to the provisioned
# table's real name, using the unit's own execution role. Uncompiled it is
# bound to a local DynamoDB.
#
# The compiler additionally grants this unit IAM access to exactly this table,
# which is the thing a hand-written boto3 program always gets slightly wrong in
# the permissive direction.
orders_table = cloudcc.persist(
    boto3.resource("dynamodb").Table("orders"),
    id="orders",
)


# ---------------------------------------------------------------------------
# 4. cassandra-driver -- proposed
# ---------------------------------------------------------------------------
#
# `Cluster(...)` is the parameters object; `cluster.connect()` is what opens
# sockets and returns a Session. So the cheapest object that names the resource
# is the Cluster, and the application still calls `connect()` itself.
#
# shim: returns a Cluster pointed at Amazon Keyspaces, with TLS and the
# SigV4 auth provider already installed -- which is several dozen lines of
# setup that every Keyspaces user writes once, badly.
#
# open question: Keyspaces has no `USE keyspace` DDL through the driver and
# rejects some `CREATE TABLE` options, so `models` here would have to mean
# "these tables must already exist" rather than "these are the tables". Same
# position as the relational side: cloudcc does not run DDL.
metrics_cluster = cloudcc.persist(
    Cluster(["127.0.0.1"], port=9042),
    id="eventMetrics",
)


# ---------------------------------------------------------------------------
# 5. clickhouse-driver -- proposed, and the first honest "no"
# ---------------------------------------------------------------------------
#
# There is no managed ClickHouse on AWS. The available answers are: run it on
# ECS or EKS with EBS, or use ClickHouse Cloud, which is a different provider
# with its own credentials. Neither is a default the compiler should pick on a
# program's behalf -- one costs an always-on cluster, the other costs an
# account somewhere else -- so `type` must be stated in cloudcc.yaml and the
# error when it is not says exactly that:
#
#     persist_columnar "analytics" has no default type: ClickHouse has no
#     managed AWS equivalent. Choose one in cloudcc.yaml:
#       type: self_hosted_ecs   -- a container and an EBS volume, always on
#       type: clickhouse_cloud  -- external; set connection.secret to a secret
#                                  holding the service credentials
#
# This is D15 and D9 doing their job. The alternative -- quietly mapping it to
# Redshift or to Athena because both are columnar -- produces a program that
# compiles, deploys, and then behaves differently, which is the failure mode
# the whole project is organised against.
#
# shim: returns a `clickhouse_driver.Client`. The driver is lazy, so whichever
# backing is chosen, nothing connects at import.
analytics = cloudcc.persist(
    ClickHouseClient(host="localhost"),
    id="analytics",
)
