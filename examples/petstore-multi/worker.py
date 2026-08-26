"""The background execution unit.

It shares shared/store.py with the api unit, which is what makes both units
resolve to the same DynamoDB table and the same SNS topic.
"""

from pathlib import Path

import cloudcompiler as cloudcc

from shared.store import events, read_pet, summarize

cloudcc.execution_unit(id="worker")

audit = cloudcc.persist(Path("./petAudit"), id="petAudit")


def on_pet_event(message: dict):
    pet_id = message.get("id")
    pet = read_pet(pet_id) or {}
    # pathlib's spelling, because that is what `audit` is: a Path locally, and
    # a cloudpathlib S3Path once compiled. Neither has a `write(name, data)`
    # method -- that belonged to an SDK class this project no longer has.
    audit.mkdir(parents=True, exist_ok=True)
    (audit / f"{pet_id}.txt").write_bytes(summarize(pet).encode("utf-8"))
    return {"audited": pet_id}


events.subscribe(on_pet_event)
