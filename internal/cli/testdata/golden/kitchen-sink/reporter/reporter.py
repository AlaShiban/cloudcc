"""A container-hosted unit: it serves a summary of what the others have done.

It reads two stores and answers one route, which is all a container can be here.
Reacting to the topic is `auditor.py`'s job: a delivery is pushed to a function,
and nothing polls on a container's behalf, so a container that subscribed would
be a handler nothing ever called.
"""
# Injected by cloudcc: runtime clients for this program's declared capabilities.
from _cloudcc_runtime import expose as _cloudcc_expose


from fastapi import FastAPI

from stores import catalogue, count_docs

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
