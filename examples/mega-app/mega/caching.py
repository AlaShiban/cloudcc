"""Caching -- one resource, one non-resource, and one that needs configuring.

The category is small but it contains the example's best candidate for a
*lint* rather than a capability: an in-process cache in a horizontally scaled
unit is correct code with surprising behaviour, and the compiler is the only
thing in the stack that knows both facts at once.
"""

from cachetools import TTLCache, cached
from dogpile.cache import make_region

import cloudcompiler as cloudcc

from .nosql import cache as redis_client

# ---------------------------------------------------------------------------
# 1. redis-py -- supported today
# ---------------------------------------------------------------------------
#
# Already declared in mega/nosql.py. Nothing to add: a cache and a store are
# the same resource, and which one it is depends only on whether the program
# sets a TTL.


def cached_item(item_id: str) -> bytes | None:
    return redis_client.get(f"item:{item_id}")


# ---------------------------------------------------------------------------
# 2. cachetools -- nothing to provision, and a lint worth having
# ---------------------------------------------------------------------------
#
# This is a dict with an eviction policy. There is no resource, no shim, and
# nothing for the compiler to rewrite.
#
# What there *is*: a module-level TTLCache in a unit that runs on more than one
# instance is per-instance, so the hit rate is divided by the instance count and
# an invalidation on one instance does not reach the others. Every developer
# knows this and everyone is bitten by it anyway, because the code looks
# identical to the single-process version that worked.
#
# proposed lint (a note, not an error -- the code is legitimate):
#
#     mega/caching.py:NN: `postcodes` is an in-process cache in unit "api",
#     which runs on up to 20 concurrent Lambda instances. Entries are not
#     shared and invalidation reaches one instance. If that is intended,
#     nothing to do; if not, persist a Redis client.
#
# The compiler can say this because it knows the unit's scaling configuration,
# which is the fact the author does not have in front of them while writing
# this line.
postcodes: TTLCache = TTLCache(maxsize=1024, ttl=300)


@cached(postcodes)
def region_for(postcode: str) -> str:
    return postcode[:2]


# ---------------------------------------------------------------------------
# 3. dogpile.cache -- proposed
# ---------------------------------------------------------------------------
#
# A region is unconfigured until `configure()` is called, and the backend is
# named by a string with its arguments in a dict. That is close to ideal: the
# region object exists immediately, and everything the shim needs to supply
# lives in the arguments.
#
# shim: returns a configured region whose backend arguments point at the
# provisioned cluster -- `dogpile.cache.redis` with the resolved host, port,
# TLS and auth token. The region's `get_or_create` semantics, which are the
# reason to choose dogpile at all, are untouched.
#
# open question: dogpile's redis backend takes a `url` or discrete arguments,
# and either way the auth token is a string in a dict that ends up in the
# region's repr. Passing an already-constructed client through
# `backend="dogpile.cache.redis"` + `arguments={"connection_pool": ...}` avoids
# that and reuses the persisted client, which is also the answer to "do I get
# two clusters" -- no, it is the same one.
prices = cloudcc.persist(make_region(), id="itemCache")
prices.configure("dogpile.cache.redis", expiration_time=60)
