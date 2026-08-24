"""A container-hosted unit: it subscribes to the topic and serves a summary."""

from fastapi import FastAPI
import cloudcompiler as cloudcc

from stores import catalogue, docs, events

cloudcc.execution_unit(id="reporter")

app = FastAPI()
cloudcc.expose(app, id="reporter-web")


@app.get("/health")
def health():
    return {"status": "ok"}


@app.get("/summary")
def summary():
    return {"items": len(catalogue.keys()), "documents": len(docs.list())}


def on_item_event(message: dict):
    docs.write(f"audit/{message['id']}.txt", message["action"].encode("utf-8"))
    return {"audited": message["id"]}


events.subscribe(on_item_event)
