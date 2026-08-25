"""A Python worker sharing the Node unit's store."""
# Injected by cloudcc: runtime clients for this program's declared capabilities.
from _cloudcc_runtime import kv as _cloudcc_kv



None

pets = _cloudcc_kv.connect("petsByOwner")


def summarise() -> int:
    return len(pets.keys())
