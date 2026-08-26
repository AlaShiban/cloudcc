"""The background execution unit.

It shares shared/store.py with the api unit, which is what makes both units
resolve to the same DynamoDB table and the same SNS topic.

What it does *not* share is shared/catalogue.py. Permissions and environment
are both derived from what a unit bundles, so a worker that never imports the
catalogue is a worker with no access to the database or the cache -- least
privilege as a consequence of the import graph rather than as a list somebody
maintains.
"""

from pathlib import Path

import cloudcompiler as cloudcc

from shared.signing import stamp
from shared.store import events, read_pet, summarize

cloudcc.execution_unit(id="worker")

audit = cloudcc.persist(Path("./petAudit"), id="petAudit")


def on_pet_event(message: dict):
    pet_id = message.get("id")
    pet = read_pet(pet_id) or {}
    # pathlib's spelling, because that is what `audit` is: a Path locally, and
    # a cloudpathlib S3Path once compiled. Neither has a `write(name, data)`
    # method -- that belonged to an SDK class this project no longer has.
    summary = summarize(pet)
    # Signed with the managed secret, which is the only thing this unit needs
    # that the api does not have.
    record = f"{summary}\nsigned: {stamp(pet_id, summary)}\n"
    audit.mkdir(parents=True, exist_ok=True)
    (audit / f"{pet_id}.txt").write_bytes(record.encode("utf-8"))
    return {"audited": pet_id}


events.subscribe(on_pet_event)
