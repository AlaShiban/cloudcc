"""A plain FastAPI app. The only CloudCompiler-specific lines are the import
and the two hint calls, which the compiler reads statically and rewrites into
real AWS clients in the compiled copy."""
# Injected by cc: runtime clients for this program's declared capabilities.
from _cc_runtime import expose as _cc_expose
from _cc_runtime import kv as _cc_kv


from fastapi import FastAPI, HTTPException

app = FastAPI()

pets = _cc_kv.connect("petsByOwner")
_cc_expose.register(app, id="pet-api")


@app.get("/pets/{pet_id}")
def get_pet(pet_id: str):
    pet = pets.get(pet_id)
    if pet is None:
        raise HTTPException(status_code=404, detail="no such pet")
    return pet


@app.put("/pets/{pet_id}")
def put_pet(pet_id: str, pet: dict):
    pets.put(pet_id, pet)
    return {"ok": True, "id": pet_id}


@app.delete("/pets/{pet_id}")
def delete_pet(pet_id: str):
    pets.delete(pet_id)
    return {"ok": True}


@app.get("/health")
def health():
    return {"status": "ok"}
