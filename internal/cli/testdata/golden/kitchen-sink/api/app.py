"""Exercises every capability cloudcc supports, in one program.

The api unit runs on Lambda behind an HTTP API; the reporter unit runs on
Fargate behind an ALB. Both are plain Python -- the only cloudcc-specific lines are
the import and the hint calls.
"""
# Injected by cloudcc: runtime clients for this program's declared capabilities.
from _cloudcc_runtime import config as _cloudcc_config
from _cloudcc_runtime import expose as _cloudcc_expose


import json

from fastapi import FastAPI, HTTPException

from stores import cache, catalogue, db, docs, events, signing_key

None

app = FastAPI()
_cloudcc_expose.register(app, id="shop-api")

log_level = _cloudcc_config.value("log_level", default="info")
stripe_key = _cloudcc_config.value("stripe_key")

# Claimed so the seed data travels with this unit even though nothing imports it.
SEED = "./data/*.json"


@app.get("/health")
def health():
    return {"status": "ok", "log_level": log_level}


@app.get("/items/{item_id}")
def get_item(item_id: str):
    cached = cache.get(item_id)
    if cached is not None:
        return json.loads(cached)
    stored = catalogue.get_item(Key={"id": item_id}).get("Item")
    if stored is None:
        raise HTTPException(status_code=404, detail="no such item")
    item = json.loads(stored["item"])
    cache.set(item_id, json.dumps(item), ex=60)
    return item


@app.put("/items/{item_id}")
def put_item(item_id: str, item: dict):
    catalogue.put_item(Item={"id": item_id, "item": json.dumps(item)})
    cache.delete(item_id)
    docs.write(f"{item_id}.json", json.dumps(item).encode("utf-8"))
    events.publish({"action": "upserted", "id": item_id})
    return {"ok": True, "id": item_id}


@app.get("/receipt/{item_id}")
def receipt(item_id: str):
    return {"signed_with": signing_key.get()[:4], "database": db.url().split("@")[-1]}
