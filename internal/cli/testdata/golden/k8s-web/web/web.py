"""A container that serves HTTP, and says nothing about how it is run.

This is the whole example. There is no Kubernetes in it -- no manifest, no
Deployment, no Service, no image reference -- because none of that is a property
of the program. `cloudcc.yaml` says `platform: kubernetes` and the same file
compiles to a Deployment behind a Service; remove that line and it compiles to a
Fargate task behind a load balancer, with this file untouched.

It deliberately reaches no store. A pod's AWS identity comes from IRSA, which
cloudcc does not emit yet, so a unit that did reach one would be warned at
compile time -- and an example is a bad place to demonstrate a gap.
"""
# Injected by cloudcc: runtime clients for this program's declared capabilities.
from _cloudcc_runtime import expose as _cloudcc_expose


import os

from fastapi import FastAPI


None

app = FastAPI()
_cloudcc_expose.register(app, id="web-front")


@app.get("/health")
def health():
    return {"status": "ok"}


@app.get("/where")
def where():
    """What the platform put around this process.

    Kubernetes sets HOSTNAME to the pod name, so this is a cheap way for a test
    to tell a pod from a Fargate task without the application knowing which it
    is running under.
    """
    return {"host": os.environ.get("HOSTNAME", "unknown")}
