"""The HTTP-facing execution unit."""

from fastapi import FastAPI, HTTPException
import cloudcompiler as cloudcc

from shared.store import pets, events, summarize

cloudcc.execution_unit(id="api")

# Claimed before execution-unit closure runs, so these assets never end up
# inside a Lambda bundle.
cloudcc.static_unit("petstore-site", static_files="./public/**/*", index_document="index.html")

app = FastAPI()
cloudcc.expose(app, id="pet-api")

log_level = cloudcc.config_value("log_level", default="info")


@app.get("/pets/{pet_id}")
def get_pet(pet_id: str):
    pet = pets.get(pet_id)
    if pet is None:
        raise HTTPException(status_code=404, detail="no such pet")
    return pet


@app.put("/pets/{pet_id}")
def put_pet(pet_id: str, pet: dict):
    pets.put(pet_id, pet)
    events.publish({"action": "created", "id": pet_id, "summary": summarize(pet)})
    return {"ok": True, "id": pet_id}


@app.get("/health")
def health():
    return {"status": "ok", "log_level": log_level}
