// Publish/subscribe, backed by SNS or SQS.
//
// Which one this is was decided when the program was compiled, from the
// requirements the topic declared -- fan-out is a topic, a single consumer is a
// queue -- and it arrives here as a binding rather than as something to work out
// from the environment. Publishing goes straight to that service. Subscribing
// registers a handler in process: the subscription itself is infrastructure,
// created by the generated Pulumi project, and the Lambda entrypoint routes
// delivered records here.
//
// The two differ in more than the client. SNS pushes a notification whose
// payload sits under `Sns.Message`; SQS is polled by Lambda on the function's
// behalf and its payload is the record's `body`. Both arrive at the same
// handlers with the same object.

import { PublishCommand, SNSClient } from "@aws-sdk/client-sns";
import { SendMessageCommand, SQSClient } from "@aws-sdk/client-sqs";

import { common, env, slug } from "./client.js";
import { emit } from "./trace.js";

const handlers = new Map();

/** The two backings this shim implements, as the compiler spells them. */
const SNS = "sns";
const SQS = "sqs";

export function connect(id) {
  const key = slug(id);
  const backing = env(`CLOUDCC_TOPIC_${key}_BACKING`, "persist", id);
  if (backing === SQS) {
    const url = env(`CLOUDCC_TOPIC_${key}_URL`, "persist", id);
    return new Topic(id, backing, url, new SQSClient(common()));
  }
  if (backing === SNS) {
    const arn = env(`CLOUDCC_TOPIC_${key}_ARN`, "persist", id);
    return new Topic(id, backing, arn, new SNSClient(common()));
  }
  // Not a fallback to SNS. A binding this shim does not implement means the
  // compiler and the runtime disagree about what was deployed, and publishing
  // to the wrong service loses messages silently.
  throw new Error(
    `topic ${JSON.stringify(id)} was compiled to ${JSON.stringify(backing)}, ` +
      "which this runtime does not implement",
  );
}

export class Topic {
  constructor(id, backing, address, client) {
    this.id = id;
    this._backing = backing;
    this._address = address;
    this._client = client;
  }

  async publish(message) {
    const body = JSON.stringify(message);
    // Recorded after the hand-off, not before: a publish that reached the
    // service and was refused -- a wrong ARN, a missing permission -- would
    // otherwise be traced as though it had happened, which is the one thing a
    // trace of a publish is for.
    try {
      if (this._backing === SQS) {
        await this._client.send(
          new SendMessageCommand({ QueueUrl: this._address, MessageBody: body }),
        );
      } else {
        await this._client.send(new PublishCommand({ TopicArn: this._address, Message: body }));
      }
    } catch (err) {
      emit("pubsub", this.id, "publish", { args: message, err: err?.name ?? "Error" });
      throw err;
    }
    emit("pubsub", this.id, "publish", { args: message });
  }

  subscribe(fn) {
    const existing = handlers.get(this.id) ?? [];
    existing.push(fn);
    handlers.set(this.id, existing);
    return fn;
  }

  subscribers() {
    return [...(handlers.get(this.id) ?? [])];
  }
}

/**
 * Whether an event looks like a delivery from a topic or a queue.
 *
 * Both shapes are a `Records` list; what tells them apart is the key each
 * record carries, and a record with neither is somebody else's event.
 */
export function isDelivery(event) {
  if (!Array.isArray(event?.Records) || event.Records.length === 0) {
    return false;
  }
  const first = event.Records[0];
  return "Sns" in first || first?.eventSource === "aws:sqs";
}

/** Deliver a Lambda event to every registered handler. */
export async function dispatch(event) {
  const results = [];
  for (const record of event.Records ?? []) {
    const raw = "Sns" in record ? (record.Sns?.Message ?? "{}") : (record.body ?? "{}");
    let message;
    try {
      message = JSON.parse(raw);
    } catch {
      message = { message: raw };
    }
    for (const [topicId, list] of handlers.entries()) {
      for (const handler of list) {
        // Recorded on the subscriber's side. Locally a topic fans out inside
        // publish(); here this is a separate invocation that may land after
        // the publisher answered, so the comparison groups by resource rather
        // than pretending one ordering exists.
        emit("pubsub", topicId, "deliver", { args: message });
        results.push(await handler(message));
      }
    }
  }
  return results;
}
