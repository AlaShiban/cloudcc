"""File store backed by S3."""

from . import _client


def connect(id):
    """Return a client for the file store declared as ``persist_fs(id)``."""
    bucket = _client.env("CC_FS_%s_BUCKET" % _client.slug(id), "persist_fs", id)
    return Bucket(id, bucket, _client.client("s3"))


class Bucket:
    def __init__(self, id, bucket, s3):
        self.id = id
        self._bucket = bucket
        self._s3 = s3

    def _key(self, key):
        return str(key).lstrip("/")

    def read(self, key):
        """Return the bytes stored at ``key``.

        Raises FileNotFoundError when absent, matching the local emulation
        rather than leaking botocore's ClientError to callers.
        """
        try:
            return self._s3.get_object(Bucket=self._bucket, Key=self._key(key))["Body"].read()
        except self._s3.exceptions.NoSuchKey:
            raise FileNotFoundError(key) from None

    def write(self, key, data):
        """Store ``data`` at ``key``."""
        if not isinstance(data, bytes):
            data = str(data).encode("utf-8")
        self._s3.put_object(Bucket=self._bucket, Key=self._key(key), Body=data)

    def delete(self, key):
        """Remove ``key`` if present."""
        self._s3.delete_object(Bucket=self._bucket, Key=self._key(key))

    def exists(self, key):
        """Whether ``key`` is present."""
        try:
            self._s3.head_object(Bucket=self._bucket, Key=self._key(key))
            return True
        except Exception:
            return False

    def list(self, prefix=""):
        """Every key under ``prefix``, sorted."""
        out = []
        token = None
        while True:
            kwargs = {"Bucket": self._bucket, "Prefix": prefix}
            if token:
                kwargs["ContinuationToken"] = token
            page = self._s3.list_objects_v2(**kwargs)
            out.extend(obj["Key"] for obj in page.get("Contents", []))
            if not page.get("IsTruncated"):
                return sorted(out)
            token = page.get("NextContinuationToken")

    def __repr__(self):
        return "<Bucket %r (s3)>" % self.id
