"""Shared by both execution units.

The same persist_kv id is referenced from one module that both units import,
so the two units end up wired to a single DynamoDB table with their own
environment bindings.
"""
# Injected by cc: runtime clients for this program's declared capabilities.
from _cc_runtime import kv as _cc_kv
from _cc_runtime import pubsub as _cc_pubsub



pets = _cc_kv.connect("petsByOwner")
events = _cc_pubsub.connect("petEvents")


def summarize(pet: dict) -> str:
    return f"{pet.get('name', 'unnamed')} ({pet.get('species', 'unknown')})"
