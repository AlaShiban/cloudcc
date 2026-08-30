"""File store backed by S3.

``connect`` returns a ``cloudpathlib.S3Path``, which mirrors ``pathlib.Path``:
the ``/`` operator, ``read_text``, ``write_bytes``, ``iterdir`` and ``exists``
all behave as they do locally. A program that declared a ``Path`` keeps working
against the same shape.
"""

from . import _client, trace


def connect(id):
    """Return an S3Path rooted at the bucket declared for ``id``."""
    bucket = _client.env("CLOUDCC_FS_%s_BUCKET" % _client.slug(id), "persist", id)

    from cloudpathlib import S3Client, S3Path

    # cloudpathlib's S3Client is not a boto3 client and does not take a boto3
    # client's keyword arguments: passing region_name to it is a TypeError at
    # import, which is where this shim runs. The region belongs on a session.
    kwargs = {"boto3_session": _client.session()}
    if _client.endpoint():
        kwargs["endpoint_url"] = _client.endpoint()

    return trace.wrap(S3Path("s3://%s" % bucket, client=S3Client(**kwargs)), "fs", id)
