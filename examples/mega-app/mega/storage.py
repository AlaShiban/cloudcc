"""Files.

Python already has the right object, so the SDK does not supply one: a
``pathlib.Path`` is a file store, and the compiled program gets a
``cloudpathlib.S3Path``, which has the same API. ``open``, ``read_bytes``,
``write_text``, ``iterdir``, ``/`` -- all of it transfers.

The asymmetry with the Node SDK is deliberate and is documented in the client
table: Node's `fs` is a set of functions with no object to wrap, so there the
SDK has to provide a `FileStore` class.
"""

from pathlib import Path

import cloudcompiler as cloudcc

# shim: `_cloudcc_runtime.fs.connect(id)` returns a `cloudpathlib.S3Path` for
# the provisioned bucket, using the unit's own role. Uncompiled this is the
# local directory it says it is, created on first use.
receipts = cloudcc.persist(Path("./receipts"), id="receipts")


def store(order_id: str, pdf: bytes) -> None:
    (receipts / f"{order_id}.pdf").write_bytes(pdf)


def recent() -> list[str]:
    return sorted(p.name for p in receipts.iterdir())


# Where the two types are *not* the same: an S3Path has `.bucket` and `.key`,
# and a Path has neither. Any code that reaches for them compiles and then
# fails locally, which is the mirror image of the usual failure and just as
# unwelcome.
#
# proposed: `cloudcc.location(receipts)` returns the physical location of a
# persisted store -- a bucket name and prefix in the cloud, a directory path
# locally -- as a small typed object. Anything that needs to name the resource
# to another AWS API (a presigned URL, a bucket policy, a Glue crawler) goes
# through that rather than through S3Path attributes, and so keeps working in
# both directions.
#
# The alternative -- letting programs reach into S3Path -- means the uncompiled
# run stops being a faithful rehearsal, and that run is the whole point.
