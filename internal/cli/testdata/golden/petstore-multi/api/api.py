"""The HTTP-facing execution unit.

It holds four kinds of state, each declared by wrapping the client that
provides it, and each becoming a different AWS service once compiled:

    a boto3 Table          -> DynamoDB      the pets themselves
    an async SQLAlchemy    -> RDS Postgres  the breed catalogue
      engine
    a redis.asyncio.Redis  -> ElastiCache   a cache in front of the reads
    a cloudcc.Topic        -> SNS           what the worker reacts to

The two asynchronous clients are not the same libraries as their synchronous
namesakes, and the compiler keeps them apart: what this unit is handed is an
AsyncEngine and an awaitable Redis, so every `await` below means the same thing
before and after compiling. The worker unit declares `create_engine` instead and
is handed the synchronous one.

Nothing below says any of those service names. The client's type decides the
capability and cloudcc.yaml chooses between variants of it, so moving the cache
to MemoryDB is a configuration change rather than a code change.
"""
# Injected by cloudcc: runtime clients for this program's declared capabilities.
from _cloudcc_runtime import config as _cloudcc_config
from _cloudcc_runtime import expose as _cloudcc_expose


from fastapi import FastAPI, HTTPException

from shared.catalogue import (
    breeds,
    cache_summary,
    cached_summary,
    forget,
    record_sighting,
)
from shared.store import delete_pet as drop_pet, events, read_pet, summarize, write_pet

None

# Claimed before execution-unit closure runs, so these assets never end up
# inside a Lambda bundle.
None

app = FastAPI()
_cloudcc_expose.register(app, id="pet-api")

log_level = _cloudcc_config.value("log_level", default="info")


@app.get("/pets/{pet_id}")
async def get_pet(pet_id: str):
    pet = read_pet(pet_id)
    if pet is None:
        raise HTTPException(status_code=404, detail="no such pet")
    # The cache is consulted for the summary only. The pet itself comes from
    # the table either way, so a stale cache costs a description rather than a
    # wrong record.
    summary = await cached_summary(pet_id)
    if summary is None:
        summary = summarize(pet)
        await cache_summary(pet_id, summary)
        cached = False
    else:
        cached = True
    return {**pet, "summary": summary, "cached": cached}


@app.put("/pets/{pet_id}")
async def put_pet(pet_id: str, pet: dict):
    write_pet(pet_id, pet)
    summary = summarize(pet)
    await cache_summary(pet_id, summary)
    seen = await record_sighting(pet.get("breed", "unrecorded"), pet.get("species", "unknown"))
    events.publish({"action": "created", "id": pet_id, "summary": summary})
    return {"ok": True, "id": pet_id, "breed_seen": seen}


@app.delete("/pets/{pet_id}")
async def delete_pet(pet_id: str):
    drop_pet(pet_id)
    await forget(pet_id)
    return {"ok": True}


@app.get("/breeds")
async def list_breeds():
    """The relational read path: a query rather than a key lookup."""
    return {"breeds": await breeds()}


@app.get("/health")
def health():
    return {"status": "ok", "log_level": log_level}
