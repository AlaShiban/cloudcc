"""Secret backed by AWS Secrets Manager."""

from . import _client


def connect(id):
    """Return a client for the secret declared as ``persist_secret(id)``."""
    arn = _client.env("CLOUDCC_SECRET_%s_ARN" % _client.slug(id), "persist", id)
    return Secret(id, arn, _client.client("secretsmanager"))


class Secret:
    def __init__(self, id, arn, sm):
        self.id = id
        self._arn = arn
        self._sm = sm

    def get(self):
        """Return the secret's current value."""
        response = self._sm.get_secret_value(SecretId=self._arn)
        if "SecretString" in response:
            return response["SecretString"]
        return response["SecretBinary"].decode("utf-8")

    def set(self, value):
        """Replace the secret's value."""
        self._sm.put_secret_value(SecretId=self._arn, SecretString=str(value))

    def __repr__(self):
        return "<Secret %r (secretsmanager)>" % self.id
