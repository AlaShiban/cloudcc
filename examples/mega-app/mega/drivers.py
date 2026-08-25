"""Relational access without an ORM -- the five raw drivers.

Same resource as mega/orm.py, different objects to hand back. The interesting
difference is *lifetime*: an ORM engine is a lazy handle, whereas most of these
libraries' primary constructors open a socket immediately. A socket opened at
import time is wrong in a Lambda -- it is opened during init, held across
freezes, and is dead by the second invocation -- so the treatment of this whole
category is governed by one preference:

    **Wrap the cheapest object that names the resource.** If the library has a
    pool or a parameters object, that is what gets persisted, not a live
    connection. If it has neither, the shim returns a connection and the
    documentation says so, because the reader deserves to know a socket is
    being opened on their behalf.
"""

import sqlite3

import asyncpg
import MySQLdb
import psycopg_pool
import pymysql

import cloudcompiler as cloudcc

# ---------------------------------------------------------------------------
# 1. psycopg (v3) -- proposed
# ---------------------------------------------------------------------------
#
# psycopg's own recommendation for a server is `psycopg_pool.ConnectionPool`,
# which is lazy: constructing it does not connect, and `open=False` makes that
# explicit. So this is the cheapest object that names the database, and it is
# what gets wrapped.
#
# shim: returns a ConnectionPool whose connection parameters resolve to the
# provisioned RDS instance. The password is supplied per-connection through the
# pool's `kwargs`/`configure` path rather than embedded in the conninfo string,
# so it is never in the pool's repr -- and psycopg puts the conninfo in the
# repr.
#
# open question: the pool computes its connection parameters once, at
# construction. Confirm whether a rotating secret can be re-read per connection
# without reconstructing the pool; if it cannot, IAM database authentication is
# the better answer for this driver, since the token is generated per connect
# by definition.
pool = cloudcc.persist(
    psycopg_pool.ConnectionPool("postgresql://localhost/mega", open=False),
    id="megaDb",
)


# ---------------------------------------------------------------------------
# 2. PyMySQL -- proposed
# ---------------------------------------------------------------------------
#
# `pymysql.connect()` opens the socket. There is no lazy object in the library,
# so persisting the connection is persisting a connection.
#
# shim: returns a real `pymysql.connections.Connection` to the provisioned RDS
# MySQL instance, opened when this module is imported. That is honest but not
# free, and the shim should therefore emit a compile-time note suggesting the
# call be moved inside the request handler, where a short-lived connection is
# the right shape. cloudcc does not move it: rewriting where a program opens
# its database connections is a change to its behaviour, not to its
# infrastructure.
mysql_conn = cloudcc.persist(
    pymysql.connect(host="localhost", user="mega", database="mega_orders"),
    id="ordersDb",
)


# ---------------------------------------------------------------------------
# 3. mysqlclient -- proposed
# ---------------------------------------------------------------------------
#
# The C driver. Identical treatment to PyMySQL and the same caveat, but a
# separate library identifier, because the returned type must be
# `MySQLdb.connections.Connection` -- handing back a PyMySQL connection would
# be almost right, which is the worst kind of wrong. They differ in exception
# classes and in `cursorclass`, and code that catches `MySQLdb.OperationalError`
# would silently stop catching anything.
#
# This is the clearest illustration of why the client table is keyed by library
# and not by capability.
legacy_conn = cloudcc.persist(
    MySQLdb.connect(host="localhost", user="mega", db="mega_legacy"),
    id="ordersDb",
)


# ---------------------------------------------------------------------------
# 4. asyncpg -- proposed
# ---------------------------------------------------------------------------
#
# `asyncpg.create_pool()` returns a Pool object that is awaited to open. It is
# not a coroutine -- it is awaitable *and* usable as an async context manager --
# so `persist` can return it synchronously and the application awaits it in its
# own startup, exactly as it would have.
#
# shim: returns a Pool configured against the provisioned instance. asyncpg
# takes `password` as either a string or a callable, and the callable may be a
# coroutine function, so the managed secret is supplied lazily with no
# contortion. Of every library in this file, asyncpg fits the design best.
async_pool = cloudcc.persist(
    asyncpg.create_pool("postgresql://localhost/mega_async"),
    id="asyncDb",
)


# ---------------------------------------------------------------------------
# 5. sqlite3 -- deliberately NOT persistable
# ---------------------------------------------------------------------------
#
# There is no correct cloud resource for this. A SQLite file needs POSIX byte
# range locks that no object store provides, and a database on a Lambda's
# ephemeral disk vanishes with the instance -- so a `persist`ed sqlite3
# connection would be either broken or a lie.
#
# So the compiler should *reject* it, with a message that says which of the
# three real answers the author wants:
#
#     persist() cannot make a sqlite3 connection durable: a SQLite file cannot
#     be shared safely between instances.
#       - for a database, use sqlalchemy.create_engine("postgresql://...")
#       - for a file that must survive, persist a pathlib.Path
#       - for a local cache, leave this call unwrapped -- it already works
#
# Unwrapped, sqlite3 is left completely alone, and that is a supported and
# useful thing to do: a read-only lookup table built during init and thrown
# away with the instance is a good use of it. cloudcc has no opinion about
# code that does not claim to be infrastructure.
lookup = sqlite3.connect(":memory:")
lookup.execute("CREATE TABLE IF NOT EXISTS postcode (code TEXT, region TEXT)")
