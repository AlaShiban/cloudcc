"""Publish/subscribe backed by SNS.

Publishing goes straight to SNS. Subscribing registers a handler in-process:
the subscription itself is infrastructure, created by the generated Pulumi
project, and the Lambda entrypoint routes the delivered records here.
"""

import json

from . import _client

#: Registered handlers, keyed by topic id, in registration order.
_HANDLERS = {}


def connect(id):
    """Return a client for the topic declared as ``pubsub_topic(id)``."""
    arn = _client.env("CC_TOPIC_%s_ARN" % _client.slug(id), "pubsub", id)
    return Topic(id, arn, _client.client("sns"))


class Topic:
    def __init__(self, id, arn, sns):
        self.id = id
        self._arn = arn
        self._sns = sns

    def publish(self, message):
        """Publish ``message`` to every subscriber of this topic."""
        self._sns.publish(TopicArn=self._arn, Message=json.dumps(message))

    def subscribe(self, fn):
        """Register ``fn`` as a subscriber. Usable as a decorator."""
        _HANDLERS.setdefault(self.id, []).append(fn)
        return fn

    def subscribers(self):
        """The registered subscribers, in registration order."""
        return tuple(_HANDLERS.get(self.id, ()))

    def __repr__(self):
        return "<Topic %r (sns)>" % self.id


def dispatch(event):
    """Deliver an SNS Lambda event to every registered handler.

    Called by the generated Lambda entrypoint for subscriber units; the message
    body is decoded back into the dict that was published.
    """
    results = []
    for record in event.get("Records", []):
        sns = record.get("Sns") or {}
        raw = sns.get("Message", "{}")
        try:
            message = json.loads(raw)
        except ValueError:
            message = {"message": raw}
        for handlers in _HANDLERS.values():
            for handler in handlers:
                results.append(handler(message))
    return results


def is_sns_event(event):
    """Whether ``event`` looks like an SNS notification delivery."""
    records = event.get("Records") if isinstance(event, dict) else None
    return bool(records) and "Sns" in records[0]
