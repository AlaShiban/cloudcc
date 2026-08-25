"""A plain FastAPI app. The only CloudCompiler-specific lines are the import
and the two hint calls, which the compiler reads statically and rewrites into
real AWS clients in the compiled copy.

The store is a boto3 ``Table``. cloudcc supplies no class of its own for it:
what ``persist`` hands back once compiled is a ``Table`` too, bound to the
provisioned table instead of the local one, so ``put_item``, ``query`` and
``batch_writer`` all work because they *are* boto3's.

Uncompiled, this needs a DynamoDB endpoint -- a local one is fine::

    docker run -p 8000:8000 amazon/dynamodb-local
    export AWS_ENDPOINT_URL=http://localhost:8000
"""

import json

import boto3
from fastapi import FastAPI, HTTPException

import cloudcompiler as cloudcc

app = FastAPI()

pets = cloudcc.persist(boto3.resource("dynamodb").Table("pets"), id="petsByOwner")
cloudcc.expose(app, id="pet-api")


@app.get("/pets/{pet_id}")
def get_pet(pet_id: str):
    item = pets.get_item(Key={"id": pet_id}).get("Item")
    if item is None:
        raise HTTPException(status_code=404, detail="no such pet")
    return json.loads(item["pet"])


@app.put("/pets/{pet_id}")
def put_pet(pet_id: str, pet: dict):
    # Stored as a JSON string rather than a nested map, so that numbers come
    # back as numbers: DynamoDB's native types would make them Decimals, and a
    # Decimal is not JSON-serialisable. The Node half of examples/mixed writes
    # the same shape, which is what lets the two languages share a table.
    pets.put_item(Item={"id": pet_id, "pet": json.dumps(pet)})
    return {"ok": True, "id": pet_id}


@app.delete("/pets/{pet_id}")
def delete_pet(pet_id: str):
    pets.delete_item(Key={"id": pet_id})
    return {"ok": True}


@app.get("/pets")
def list_pets():
    page = pets.scan(ProjectionExpression="id")
    return {"keys": sorted(item["id"] for item in page.get("Items", []))}


@app.get("/health")
def health():
    return {"status": "ok"}
