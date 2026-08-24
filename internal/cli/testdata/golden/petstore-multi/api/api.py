"""The HTTP-facing execution unit."""
# Injected by cc: runtime clients for this program's declared capabilities.
from _cc_runtime import config as _cc_config
from _cc_runtime import expose as _cc_expose


from fastapi import FastAPI, HTTPException

from shared.store import pets, events, summarize

None

# Claimed before execution-unit closure runs, so these assets never end up
# inside a Lambda bundle.
None

app = FastAPI()
_cc_expose.register(app, id="pet-api")

log_level = _cc_config.value("log_level", default="info")


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
