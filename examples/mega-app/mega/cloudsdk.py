"""boto3, preconfigured -- so that it can just be used.

Nothing in this file is declared, and that is the point. A unit already has an
execution role, a region, and credentials that rotate without anyone thinking
about it. So a plain `boto3.client("s3")` in a compiled unit works, with no
configuration and no `AWS_ACCESS_KEY_ID` anywhere.

The work is on the *uncompiled* side, where none of that is true. Making the
same line work in both directions is what this category is about:

    shim: the SDK sets AWS_ENDPOINT_URL, AWS_DEFAULT_REGION and a pair of dummy
    credentials to point boto3 at the local emulator, so an unmodified
    `boto3.client(...)` in a local run talks to the same emulator the
    differential harness deploys against. In a compiled unit it sets none of
    them, because the environment is already correct.

One boundary worth restating, because it is enforced by a test: **the cloudcc
SDK never imports boto3.** Cloud access appears only in the injected runtime,
and in application code like this file. A developer who has not deployed
anything should be able to `pip install cloudcompiler` and run their program
without an AWS SDK on their machine.
"""

import boto3

import cloudcompiler as cloudcc

from .storage import receipts

# shim: none. The unit's role already covers whatever this client is used for
# -- as long as the compiler knew to grant it, which is the interesting part.
s3 = boto3.client("s3")
sqs = boto3.client("sqs")


def presign_upload(key: str) -> str:
    """A presigned URL for a managed bucket.

    The bucket's physical name is chosen by the compiler, so it cannot be
    written here as a literal. `cloudcc.location()` is the proposed way to ask
    for it -- see the note at the bottom of mega/storage.py for why reaching
    into `S3Path.bucket` is not good enough.
    """
    where = cloudcc.location(receipts)
    return s3.generate_presigned_url(
        "put_object",
        Params={"Bucket": where.bucket, "Key": key},
        ExpiresIn=900,
    )


def drain(queue_url: str) -> list[dict]:
    """A queue cloudcc did not create.

    This is legitimate and should keep working: not every resource an
    application touches is one the application owns. What the compiler should
    do is *notice*, because the two failure modes are common and both are
    silent:
    """
    return sqs.receive_message(QueueUrl=queue_url).get("Messages", [])


# proposed diagnostics for raw AWS SDK usage:
#
#   1. A literal that matches a managed resource. If a program passes
#      Bucket="mega-receipts" and the compiler provisioned a bucket whose
#      physical name is "mega-receipts-a1b2c3", the literal is wrong in the
#      deployed program and right locally -- the worst possible split. Warn,
#      and name the binding to use instead.
#
#   2. A call to a service the unit has no permission for. The compiler knows
#      the unit's policy because it wrote it, and it can see `sqs.` on an
#      unmanaged queue url here. It cannot grant permission it was not asked
#      for -- guessing at IAM is how roles end up with wildcards -- but it can
#      say so at compile time instead of at runtime:
#
#        mega/cloudsdk.py:NN: unit "api" calls sqs:ReceiveMessage, but its role
#        grants no sqs permissions. Add it to cloudcc.yaml under
#        execution_units.api.iam, or persist the queue to have it managed.
#
# Neither diagnostic changes what is deployed. They exist because "it worked
# locally" is the sentence this project is written to eliminate.
