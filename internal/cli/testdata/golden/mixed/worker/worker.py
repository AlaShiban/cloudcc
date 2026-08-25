"""A Python worker sharing the Node unit's store."""
# Injected by cloudcc: runtime clients for this program's declared capabilities.
from _cloudcc_runtime import kv as _cloudcc_kv


import boto3


None

# The Node unit in api.js declares the same id, which is what makes both units
# resolve to one table. What each language holds is its own AWS client -- a
# boto3 Table here, a DynamoDBClient there -- and what they agree on is the
# item shape, explicitly.
pets = _cloudcc_kv.connect("petsByOwner")


def summarise() -> int:
    return len(pets.scan(ProjectionExpression="id").get("Items", []))
