"""The key the worker stamps audit records with.

A secret is one of the two capabilities this SDK still supplies a class for,
and for the same reason as a topic: there is no client library to wrap. A
secret is a value the environment holds, not a store with an API.

Imported by the worker alone. The api writes pets; the worker signs the record
of it having happened, and neither needs what the other holds -- which is why
they are separate modules rather than one shared one.
"""

import hashlib
import hmac

import cloudcompiler as cloudcc

audit_key = cloudcc.persist(cloudcc.Secret(), id="auditKey")


def stamp(pet_id: str, summary: str) -> str:
    """A short signature over an audit record.

    Truncated, because what it is for here is showing that the secret was
    fetched and used rather than being a real integrity guarantee -- a full
    HMAC would say exactly as much about the wiring and be harder to read in a
    test's output.
    """
    digest = hmac.new(
        audit_key.get().encode("utf-8"),
        f"{pet_id}:{summary}".encode("utf-8"),
        hashlib.sha256,
    )
    return digest.hexdigest()[:16]
