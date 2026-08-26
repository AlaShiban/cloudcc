"""A container-hosted unit: it subscribes to the topic and serves a summary."""

from fastapi import FastAPI
import cloudcompiler as cloudcc

from stores import catalogue, count_docs, events, write_doc

cloudcc.execution_unit(id="reporter")

app = FastAPI()
cloudcc.expose(app, id="reporter-web")


@app.get("/health")
def health():
    return {"status": "ok"}


@app.get("/summary")
def summary():
    stored = catalogue.scan(ProjectionExpression="id").get("Items", [])
    return {"items": len(stored), "documents": count_docs()}


def on_item_event(message: dict):
    write_doc(f"audit/{message['id']}.txt", message["action"].encode("utf-8"))
    return {"audited": message["id"]}


events.subscribe(on_item_event)
