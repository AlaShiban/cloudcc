"""Publish/subscribe, backed by SNS or SQS.

Which one this is was decided when the program was compiled, from the
requirements the topic declared -- fan-out is a topic, a single consumer is a
queue -- and it arrives here as a binding rather than as something to work out
from the environment. Publishing goes straight to that service. Subscribing
registers a handler in-process: the subscription itself is infrastructure,
created by the generated Pulumi project, and the Lambda entrypoint routes the
delivered records here.

The two differ in more than the client. SNS pushes a notification whose payload
sits under ``Sns.Message``; SQS is polled by Lambda on the function's behalf and
its payload is the record's ``body``. Both arrive at the same handlers with the
same dict, which is the whole point of the requirement being declared rather
than the service being named.
"""

import asyncio
import inspect
import json

from . import _client, trace

#: Registered handlers, keyed by topic id, in registration order.
_HANDLERS = {}

#: The two backings this shim implements, as the compiler spells them.
SNS = "sns"
SQS = "sqs"


def connect(id):
    """Return a client for the topic declared as ``persist(Topic(), id=...)``."""
    slug = _client.slug(id)
    backing = _client.env("CLOUDCC_TOPIC_%s_BACKING" % slug, "pubsub", id)
    if backing == SQS:
        url = _client.env("CLOUDCC_TOPIC_%s_URL" % slug, "pubsub", id)
        return Topic(id, backing, url, _client.client("sqs"))
    if backing == SNS:
        arn = _client.env("CLOUDCC_TOPIC_%s_ARN" % slug, "pubsub", id)
        return Topic(id, backing, arn, _client.client("sns"))
    # Not a fallback to SNS. A binding this shim does not implement means the
    # compiler and the runtime disagree about what was deployed, and publishing
    # to the wrong service loses messages silently.
    raise RuntimeError(
        "topic %r was compiled to %r, which this runtime does not implement" % (id, backing)
    )


class Topic:
    def __init__(self, id, backing, address, client):
        self.id = id
        self._backing = backing
        self._address = address
        self._client = client

    def publish(self, message):
        """Publish ``message`` to every subscriber of this topic."""
        body = json.dumps(message)
        # Recorded after the hand-off, not before: a publish that reached the
        # service and was refused -- a wrong ARN, a missing permission -- would
        # otherwise be traced as though it had happened, which is the one thing
        # a trace of a publish is for.
        try:
            if self._backing == SQS:
                self._client.send_message(QueueUrl=self._address, MessageBody=body)
            else:
                self._client.publish(TopicArn=self._address, Message=body)
        except Exception as exc:
            trace.emit("pubsub", self.id, "publish", args=message,
                       err=type(exc).__name__)
            raise
        trace.emit("pubsub", self.id, "publish", args=message)

    def subscribe(self, fn):
        """Register ``fn`` as a subscriber. Usable as a decorator."""
        _HANDLERS.setdefault(self.id, []).append(fn)
        return fn

    def subscribers(self):
        """The registered subscribers, in registration order."""
        return tuple(_HANDLERS.get(self.id, ()))

    def __repr__(self):
        return "<Topic %r (%s)>" % (self.id, self._backing)


def dispatch(event):
    """Deliver a Lambda event to every registered handler.

    Called by the generated Lambda entrypoint for subscriber units; the message
    body is decoded back into the dict that was published.
    """
    results = []
    for message in _messages(event):
        for topic_id, handlers in _HANDLERS.items():
            for handler in handlers:
                # Recorded on the subscriber's side, matching what the local
                # Topic records inside publish(). The two orderings differ --
                # locally this runs inside the publish, here it is a separate
                # invocation that may land after the publisher answered -- so
                # the comparison groups events by resource rather than
                # pretending there is one stream.
                trace.emit("pubsub", topic_id, "deliver", args=message)
                results.append(_run(handler(message)))
    return results


def _messages(event):
    """The published payloads carried by one delivery, in order."""
    for record in event.get("Records", []):
        if "Sns" in record:
            raw = (record.get("Sns") or {}).get("Message", "{}")
        else:
            raw = record.get("body", "{}")
        try:
            yield json.loads(raw)
        except ValueError:
            yield {"message": raw}


def _run(result):
    """Finish a handler that turned out to be a coroutine.

    A subscriber that calls another execution unit is ``async def``, because
    that call is a network round trip. Returning the coroutine unawaited would
    leave the handler looking as though it had run: no write, no error, and a
    warning on a stream nobody reads.

    The Lambda handler this is reached through is synchronous, so there is no
    loop already running to join.
    """
    if inspect.iscoroutine(result):
        return asyncio.run(result)
    return result


def is_delivery(event):
    """Whether ``event`` looks like a delivery from a topic or a queue.

    Both shapes are a ``Records`` list; what tells them apart is the key each
    record carries, and a record with neither is somebody else's event.
    """
    records = event.get("Records") if isinstance(event, dict) else None
    if not records:
        return False
    first = records[0]
    return "Sns" in first or first.get("eventSource") == "aws:sqs"
