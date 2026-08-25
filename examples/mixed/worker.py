"""A Python worker sharing the Node unit's store."""

import boto3

import cloudcompiler as cloudcc

cloudcc.execution_unit("worker")

# The Node unit in api.js declares the same id, which is what makes both units
# resolve to one table. What each language holds is its own AWS client -- a
# boto3 Table here, a DynamoDBClient there -- and what they agree on is the
# item shape, explicitly.
pets = cloudcc.persist(boto3.resource("dynamodb").Table("pets"), id="petsByOwner")


def summarise() -> int:
    return len(pets.scan(ProjectionExpression="id").get("Items", []))
