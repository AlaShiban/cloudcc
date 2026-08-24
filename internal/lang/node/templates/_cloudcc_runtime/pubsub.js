// Publish/subscribe backed by SNS.
//
// Publishing goes straight to SNS. Subscribing registers a handler in process:
// the subscription itself is infrastructure, created by the generated Pulumi
// project, and the Lambda entrypoint routes delivered records here.

import { PublishCommand, SNSClient } from "@aws-sdk/client-sns";

import { common, env, slug } from "./client.js";

const handlers = new Map();

export function connect(id) {
  const arn = env(`CLOUDCC_TOPIC_${slug(id)}_ARN`, "persist", id);
  return new Topic(id, arn, new SNSClient(common()));
}

export class Topic {
  constructor(id, arn, client) {
    this.id = id;
    this._arn = arn;
    this._client = client;
  }

  async publish(message) {
    await this._client.send(
      new PublishCommand({ TopicArn: this._arn, Message: JSON.stringify(message) }),
    );
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

/** Whether an event looks like an SNS notification delivery. */
export function isSnsEvent(event) {
  return Array.isArray(event?.Records) && event.Records.length > 0 && "Sns" in event.Records[0];
}

/** Deliver an SNS Lambda event to every registered handler. */
export async function dispatch(event) {
  const results = [];
  for (const record of event.Records ?? []) {
    const raw = record.Sns?.Message ?? "{}";
    let message;
    try {
      message = JSON.parse(raw);
    } catch {
      message = { message: raw };
    }
    for (const list of handlers.values()) {
      for (const handler of list) {
        results.push(await handler(message));
      }
    }
  }
  return results;
}
