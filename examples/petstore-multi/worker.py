"""The background execution unit.

It shares shared/store.py with the api unit, which is what makes both units
resolve to the same DynamoDB table and the same queue.

What it does *not* share is shared/catalogue.py. Permissions and environment
are both derived from what a unit bundles, so a worker that never imports the
catalogue is a worker with no access to the api's database or its cache -- least
privilege as a consequence of the import graph rather than as a list somebody
maintains.

It has a relational store of its own, in shared/ledger.py, and reaches it
through the *synchronous* SQLAlchemy engine -- the other half of the pair the
api unit's async engine belongs to.
"""

from pathlib import Path

import cloudcompiler as cloudcc

from shared.ledger import audited, record
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
    signature = stamp(pet_id, summary)
    line = f"{summary}\nsigned: {signature}\n"
    audit.mkdir(parents=True, exist_ok=True)
    (audit / f"{pet_id}.txt").write_bytes(line.encode("utf-8"))
    # The same fact in the ledger, where it can be counted and queried. The
    # bucket holds the document; the database holds the index.
    revision = record(pet_id, summary, signature)
    return {"audited": pet_id, "revision": revision, "ledger": audited()}


events.subscribe(on_pet_event)
