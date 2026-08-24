"""A container-hosted unit: it subscribes to the topic and serves a summary."""
# Injected by cloudcc: runtime clients for this program's declared capabilities.
from _cloudcc_runtime import expose as _cloudcc_expose


from fastapi import FastAPI

from stores import catalogue, docs, events

None

app = FastAPI()
_cloudcc_expose.register(app, id="reporter-web")


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
