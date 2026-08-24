"""Exercises every capability cc supports, in one program.

The api unit runs on Lambda behind an HTTP API; the reporter unit runs on
Fargate behind an ALB. Both are plain Python -- the only cc-specific lines are
the import and the hint calls.
"""

import json

from fastapi import FastAPI, HTTPException
import cloudcompiler as cc

from stores import cache, catalogue, db, docs, events, signing_key

cc.execution_unit(id="api")

app = FastAPI()
cc.expose(app, id="shop-api")

log_level = cc.config_value("log_level", default="info")
stripe_key = cc.config_value("stripe_key", secret=True)

# Claimed so the seed data travels with this unit even though nothing imports it.
SEED = cc.embed_assets("./data/*.json")


@app.get("/health")
def health():
    return {"status": "ok", "log_level": log_level}


@app.get("/items/{item_id}")
def get_item(item_id: str):
    cached = cache.get(item_id)
    if cached is not None:
        return json.loads(cached)
    item = catalogue.get(item_id)
    if item is None:
        raise HTTPException(status_code=404, detail="no such item")
    cache.set(item_id, json.dumps(item), ex=60)
    return item


@app.put("/items/{item_id}")
def put_item(item_id: str, item: dict):
    catalogue.put(item_id, item)
    cache.delete(item_id)
    docs.write(f"{item_id}.json", json.dumps(item).encode("utf-8"))
    events.publish({"action": "upserted", "id": item_id})
    return {"ok": True, "id": item_id}


@app.get("/receipt/{item_id}")
def receipt(item_id: str):
    return {"signed_with": signing_key.get()[:4], "database": db.url().split("@")[-1]}
