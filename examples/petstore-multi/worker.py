"""The background execution unit.

It shares shared/store.py with the api unit, which is what makes both units
resolve to the same DynamoDB table and the same SNS topic.
"""

import cloudcompiler as cloudcc

from shared.store import pets, events, summarize

cloudcc.execution_unit(id="worker")

audit = cloudcc.persist_fs("petAudit")


def on_pet_event(message: dict):
    pet_id = message.get("id")
    pet = pets.get(pet_id) or {}
    audit.write(f"{pet_id}.txt", summarize(pet).encode("utf-8"))
    return {"audited": pet_id}


events.subscribe(on_pet_event)
