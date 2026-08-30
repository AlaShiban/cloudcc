// Notifications: the outbox.
//
// Subscribes to both topics and writes a record for each. It calls nothing and
// nothing calls it, which is what a notification service should look like:
// adding it changed no other unit, and removing it would change none either.
//
// Its store is a bucket rather than a table, declared by wrapping an S3Client.
// The client is not bound to a bucket -- the name travels in each command -- so
// the program keeps writing the name it chose and the shim rewrites it to the
// provisioned one.

// Injected by cloudcc: runtime clients for this program's declared capabilities.
import * as _cloudccFs from "../../_cloudcc_runtime/fs.js";

import { PutObjectCommand, S3Client } from "@aws-sdk/client-s3";

import { courierAssigned, orderPlaced, type CourierAssigned, type OrderPlaced } from "./events";
import { describe } from "./menu";

undefined;

const BUCKET = "nomnom-outbox" as const;

const outbox = _cloudccFs.connect("notifications") as S3Client;

async function write(name: string, payload: Record<string, unknown>): Promise<void> {
  await outbox.send(
    new PutObjectCommand({
      Bucket: BUCKET,
      Key: name,
      Body: JSON.stringify(payload, Object.keys(payload).sort()),
      ContentType: "application/json",
    }),
  );
}

async function onOrderPlaced(message: OrderPlaced) {
  const orderId = message.order_id ?? "unknown";
  await write(`${orderId}-placed.json`, {
    kind: "order_placed",
    order_id: orderId,
    basket: describe(message.items ?? []),
    total_cents: message.total_cents,
  });
  return { notified: orderId };
}

async function onCourierAssigned(message: CourierAssigned) {
  const orderId = message.order_id ?? "unknown";
  await write(`${orderId}-courier.json`, {
    kind: "courier_assigned",
    order_id: orderId,
    courier: message.courier,
  });
  return { notified: orderId };
}

orderPlaced.subscribe(onOrderPlaced);
courierAssigned.subscribe(onCourierAssigned);
