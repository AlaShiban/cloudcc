"""Relational access through an ORM -- four of them, one database each.

A real application uses one. This file uses four so that every mapping is
written down somewhere, and so the shape of the problem is visible: an ORM is
declared by wrapping *the object that holds the connection settings*, and the
shim hands back an object of the same type pointed at the provisioned database.

The rule that governs every entry here, and the reason Sequelize is
unsupported in the Node table, is:

    ``persist`` is synchronous and returns the library's own type. A client
    that can only be produced by awaiting something cannot be returned from
    it -- the compiled binding would be a coroutine where the uncompiled one
    is a client, and every call site would have to change.

So for each library the load-bearing question is the same: **can the password
be supplied late?** If it can, the shim returns a real client immediately and
resolves the managed secret on first connect. If it cannot, the library needs
a different treatment, and saying so is better than shipping a bundle that
fails on its first query.
"""

from peewee import PostgresqlDatabase
from sqlalchemy import create_engine
from sqlmodel import create_engine as create_sqlmodel_engine
from tortoise import Tortoise  # noqa: F401

import cloudcompiler as cloudcc

# ---------------------------------------------------------------------------
# 1. SQLAlchemy -- supported today
# ---------------------------------------------------------------------------
#
# The URL's scheme picks the engine: postgresql:// asks for RDS Postgres,
# mysql:// for RDS MySQL. The host in it is the *local* host, and is never
# deployed -- it is what the uncompiled program connects to.
#
# shim: `_cloudcc_runtime.orm.connect(id, library="sqlalchemy")` returns a real
# sqlalchemy.Engine built from the resolved endpoint, with the password
# supplied by a `creator`/event hook rather than baked into the URL, so the
# managed secret is fetched on first connect and never appears in a repr, a
# log line, or an exception's message.
#
# `models` lists the tables the program expects. It is a hint, not a
# migration: cloudcc does not run DDL. What it buys is a compile-time check
# that the code and the declared schema have not drifted apart.
engine = cloudcc.persist(
    create_engine("postgresql+psycopg://localhost/mega"),
    id="megaDb",
    models=["Order", "Customer", "Refund"],
)


# ---------------------------------------------------------------------------
# 2. SQLModel -- proposed
# ---------------------------------------------------------------------------
#
# SQLModel is SQLAlchemy underneath, and `sqlmodel.create_engine` returns a
# SQLAlchemy Engine. So the shim is the SQLAlchemy one and the resource is the
# same. The library identifier still has to be distinct, for two reasons: the
# compiler should return the engine through sqlmodel's own constructor so the
# type the user sees is the type they wrote, and `models` can be *discovered*
# rather than declared, because every table is a SQLModel subclass with
# `table=True` and they are all in `SQLModel.metadata`.
#
# That discovery is the interesting part: this is the one ORM where the
# declared schema does not have to be repeated in the persist call, so the
# drift check comes for free.
checkout_engine = cloudcc.persist(
    create_sqlmodel_engine("postgresql://localhost/mega_checkout"),
    id="checkoutDb",
)


# ---------------------------------------------------------------------------
# 3. Peewee -- proposed
# ---------------------------------------------------------------------------
#
# Peewee's Database object is constructed with the connection settings but does
# not connect until asked, and it exposes `init()` to replace those settings
# afterwards. Both are what the shim needs.
#
# shim: returns a PostgresqlDatabase already `init()`-ed against the resolved
# endpoint. The password cannot be a callable -- peewee passes its kwargs
# straight to psycopg -- so the shim resolves the secret inside a `connect()`
# override, which peewee calls on first use and on every reconnect. That keeps
# the return type exactly `PostgresqlDatabase` while still fetching the secret
# late.
#
# open question: peewee's connection state is thread-local, and a Lambda
# container serving concurrent invocations reuses the process. Confirm the
# reconnect path is safe under `CONNECTION_LIMIT`-style reuse, or return a
# `PooledPostgresqlDatabase` instead -- which is a different type, and so a
# different table entry rather than a silent substitution.
reporting_db = cloudcc.persist(
    PostgresqlDatabase("mega_reporting", host="localhost", user="mega"),
    id="reportingDb",
    models=["DailySales"],
)


# ---------------------------------------------------------------------------
# 4. Tortoise ORM -- proposed
# ---------------------------------------------------------------------------
#
# Tortoise is the awkward one: there is no object at all until
# `await Tortoise.init(...)`, and that is a coroutine. But the coroutine is
# called by the *user*, in their own async startup, not by `persist` -- so the
# sync-return rule is not violated. What `persist` wraps is the config, which
# is pure data and available immediately.
#
# shim: returns a config mapping whose `db_url` names the provisioned
# database. The credential goes in through Tortoise's `credentials` form rather
# than the URL string, so it is not concatenated into something that gets
# logged on a connection error.
#
# The application still writes `await Tortoise.init(config=tortoise_config)`
# in its startup hook. cloudcc does not take that over: a compiler that
# silently ran a framework's initialisation would be a framework.
tortoise_config = cloudcc.persist(
    cloudcc.TortoiseConfig(
        db_url="postgres://localhost/mega_async",
        models={"models": ["mega.async_models"]},
    ),
    id="asyncDb",
)


# ---------------------------------------------------------------------------
# Deliberately absent: the Django ORM
# ---------------------------------------------------------------------------
#
# Django's ORM is synchronous by default, and its queryset attribute access
# issues queries implicitly -- `order.customer.name` is a round trip that does
# not look like one. In a unit that can serve requests concurrently, that is a
# design cloudcc should not make easier to reach for: the blocking call is
# invisible at the call site, so the cost of it lands on whoever is debugging
# tail latency rather than on whoever wrote it.
#
# So there is no `persist` mapping for it, and there is no plan for one. Django
# itself remains supported as a *web framework* -- see admin.py -- with its
# data access through one of the four ORMs above.
#
# This is the same shape of decision as Sequelize on the Node side: a library
# is left out because of a property of its API, and the way back in is to fix
# that property, not to add a table entry.
