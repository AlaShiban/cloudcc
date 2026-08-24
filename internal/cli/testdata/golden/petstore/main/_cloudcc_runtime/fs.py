"""File store backed by S3.

``connect`` returns a ``cloudpathlib.S3Path``, which mirrors ``pathlib.Path``:
the ``/`` operator, ``read_text``, ``write_bytes``, ``iterdir`` and ``exists``
all behave as they do locally. A program that declared a ``Path`` keeps working
against the same shape.
"""

from . import _client


def connect(id):
    """Return an S3Path rooted at the bucket declared for ``id``."""
    bucket = _client.env("CLOUDCC_FS_%s_BUCKET" % _client.slug(id), "persist", id)

    from cloudpathlib import S3Client, S3Path

    return S3Path("s3://%s" % bucket, client=S3Client(**_client.aws_kwargs()))
