"""A container-hosted unit: it subscribes to the topic and serves a summary."""
# Injected by cloudcc: runtime clients for this program's declared capabilities.
from _cloudcc_runtime import expose as _cloudcc_expose


from fastapi import FastAPI

from stores import catalogue, count_docs, events, write_doc

None

app = FastAPI()
_cloudcc_expose.register(app, id="reporter-web")


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
