"""A container-hosted unit: it serves a summary of what the others have done.

It reads two stores and answers one route, which is all a container can be here.
Reacting to the topic is `auditor.py`'s job: a delivery is pushed to a function,
and nothing polls on a container's behalf, so a container that subscribed would
be a handler nothing ever called.
"""

from fastapi import FastAPI
import cloudcompiler as cloudcc

from stores import catalogue, count_docs

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
