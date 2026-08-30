"""Secret backed by AWS Secrets Manager."""

from . import _client, trace


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
        """Return the secret's current value.

        A secret that has never been given one raises, and the message says so
        rather than passing on the API's. The compiler provisions the secret
        and deliberately not its contents -- a value in the generated project
        would be a value in the state file and in the repository -- so an
        unset secret means the deploy is not finished, which is a different
        problem from the one ResourceNotFoundException describes.
        """
        try:
            response = self._sm.get_secret_value(SecretId=self._arn)
        except Exception as exc:
            if type(exc).__name__ in ("ResourceNotFoundException", "InvalidRequestException"):
                raise RuntimeError(
                    "secret %r exists but has no value. cloudcc provisions the "
                    "secret and never its contents, so that nothing sensitive is "
                    "in the generated project or in the state file -- setting it "
                    "is a deploy-time step:\n"
                    "    aws secretsmanager put-secret-value --secret-id %s "
                    "--secret-string ..." % (self.id, self._arn)
                ) from None
            raise
        if "SecretString" in response:
            value = response["SecretString"]
        else:
            value = response["SecretBinary"].decode("utf-8")
        # Length, never the value: a trace goes to stderr and on to CloudWatch
        # (D21). What is worth recording is that the read happened and found
        # something, which is what tells a working binding from one quietly
        # yielding "".
        trace.emit("secret", self.id, "get", ret="<secret:%d>" % len(value))
        return value

    def set(self, value):
        """Replace the secret's value."""
        trace.emit("secret", self.id, "set", args={"len": len(str(value))})
        self._sm.put_secret_value(SecretId=self._arn, SecretString=str(value))

    def __repr__(self):
        return "<Secret %r (secretsmanager)>" % self.id
