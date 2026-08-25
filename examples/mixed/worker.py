"""A Python worker sharing the Node unit's store."""

import cloudcompiler as cloudcc

cloudcc.execution_unit("worker")

pets = cloudcc.persist(cloudcc.KVStore(), id="petsByOwner")


def summarise() -> int:
    return len(pets.keys())
