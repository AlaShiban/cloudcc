"""Shared by both execution units.

The same persist_kv id is referenced from one module that both units import,
so the two units end up wired to a single DynamoDB table with their own
environment bindings.
"""
# Injected by cloudcc: runtime clients for this program's declared capabilities.
from _cloudcc_runtime import kv as _cloudcc_kv
from _cloudcc_runtime import pubsub as _cloudcc_pubsub



pets = _cloudcc_kv.connect("petsByOwner")
events = _cloudcc_pubsub.connect("petEvents")


def summarize(pet: dict) -> str:
    return f"{pet.get('name', 'unnamed')} ({pet.get('species', 'unknown')})"
