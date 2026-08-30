"""Shared by both execution units.

The same persisted id is referenced from one module that both units import,
so the two units end up wired to a single DynamoDB table with their own
environment bindings.
"""

import json

import boto3

import cloudcompiler as cloudcc

pets = cloudcc.persist(boto3.resource("dynamodb").Table("pets"), id="petsByOwner")

#: One subscriber -- the worker, and nothing else listens -- so this is a queue
#: rather than a fan-out, and the compiler resolves it to SQS. Nothing here says
#: SQS: what is declared is the requirement, and a second listener later is a
#: change to this line rather than to either unit's code.
events = cloudcc.persist(cloudcc.Topic(subscribers="one"), id="petEvents")


def summarize(pet: dict) -> str:
    return f"{pet.get('name', 'unnamed')} ({pet.get('species', 'unknown')})"


def read_pet(pet_id: str) -> dict | None:
    """Both units read the table the same way, so the shape lives here."""
    item = pets.get_item(Key={"id": pet_id}).get("Item")
    return json.loads(item["pet"]) if item else None


def write_pet(pet_id: str, pet: dict) -> None:
    pets.put_item(Item={"id": pet_id, "pet": json.dumps(pet)})


def delete_pet(pet_id: str) -> None:
    pets.delete_item(Key={"id": pet_id})
