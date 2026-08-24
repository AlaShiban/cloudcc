"""A plain FastAPI app. The only CloudCompiler-specific lines are the import
and the two hint calls, which the compiler reads statically and rewrites into
real AWS clients in the compiled copy."""

from fastapi import FastAPI, HTTPException
import cloudcompiler as cloudcc

app = FastAPI()

pets = cloudcc.persist(cloudcc.KVStore(), id="petsByOwner")
cloudcc.expose(app, id="pet-api")


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
