"""Shared by both execution units.

The same persist_kv id is referenced from one module that both units import,
so the two units end up wired to a single DynamoDB table with their own
environment bindings.
"""

import cloudcompiler as cc

pets = cc.persist_kv("petsByOwner")
events = cc.pubsub_topic("petEvents")


def summarize(pet: dict) -> str:
    return f"{pet.get('name', 'unnamed')} ({pet.get('species', 'unknown')})"
