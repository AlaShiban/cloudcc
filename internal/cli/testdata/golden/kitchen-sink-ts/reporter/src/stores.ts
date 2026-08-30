// Every stateful capability, declared by wrapping the client that provides it.
//
// The type of what you hand to `persist` is what decides the resource. An
// ioredis client asks for ElastiCache; a `pg` Pool pointed at Postgres asks for
// RDS Postgres; a DynamoDBClient asks for DynamoDB. Uncompiled, these are
// exactly the clients you see -- a real local Redis, a real local database, a
// local DynamoDB -- and `persist` hands each one straight back.
//
// Note what is not here: a class of cloudcc's own for any of them. Every store
// below is the library's own client, which is why the compiled program can use
// every method those libraries have rather than the handful someone thought to
// wrap. In TypeScript that is visible in the types: the compiled copy of this
// file says `as DynamoDBClient`, `as Pool`, `as Redis`, so an editor opened on
// the *output* knows exactly as much as one opened on the input.

// Injected by cloudcc: runtime clients for this program's declared capabilities.
import * as _cloudccFs from "../_cloudcc_runtime/fs.js";
import * as _cloudccKv from "../_cloudcc_runtime/kv.js";
import * as _cloudccOrm from "../_cloudcc_runtime/orm_pg.js";
import * as _cloudccPubsub from "../_cloudcc_runtime/pubsub.js";
import * as _cloudccRedis from "../_cloudcc_runtime/redis_ioredis.js";
import * as _cloudccSecret from "../_cloudcc_runtime/secret.js";

import {
  DynamoDBClient,
  GetItemCommand,
  PutItemCommand,
  ScanCommand,
} from "@aws-sdk/client-dynamodb";
import {
  ListObjectsV2Command,
  PutObjectCommand,
  S3Client,
} from "@aws-sdk/client-s3";
import Redis from "ioredis";
import { Pool } from "pg";

export const CATALOGUE = "catalogue" as const;
// A *bucket* name, not the capability id, and the difference matters in
// TypeScript in a way it does not in Python: the Python mirror wraps a
// `pathlib.Path("./itemDocs")`, which is a directory, and a directory may be
// spelled however you like. Here the same store is an S3Client and the name
// travels in every command, so it has to be a legal bucket name -- lowercase,
// no underscores. Uncompiled that is the bucket the program really talks to;
// compiled, the shim rewrites it to the provisioned one.
export const DOCS = "item-docs" as const;

export const catalogue = _cloudccKv.connect("catalogue") as DynamoDBClient;
export const docs = _cloudccFs.connect("itemDocs") as S3Client;
export const signingKey = _cloudccSecret.connect("signingKey");
export const db = _cloudccOrm.connect("shopdb") as Pool;
export const cache = _cloudccRedis.connect("itemCache") as Redis;
/** What travels on the topic. */
export interface ItemEvent {
  action: string;
  id: string;
}

// The type parameter is the channel's, not the handler's. A bare `new Topic()`
// carries `Json` and a subscriber has to accept anything; naming the type here
// means the publisher and every subscriber are checked against one another,
// which is the whole reason to have the type at all.
export const events = _cloudccPubsub.connect("itemEvents");

// Both units write documents, so the spelling lives here once. An S3Client is
// not bound to a bucket -- the name travels in each command -- so the program
// keeps writing the name it chose and the shim rewrites it to the provisioned
// one.
export async function writeDoc(name: string, data: string): Promise<void> {
  await docs.send(
    new PutObjectCommand({ Bucket: DOCS, Key: name, Body: data }),
  );
}

/** How many documents exist. Empty is a count, not an error. */
export async function countDocs(): Promise<number> {
  const out = await docs.send(new ListObjectsV2Command({ Bucket: DOCS }));
  return out.KeyCount ?? 0;
}

export async function readItem(id: string): Promise<unknown | null> {
  const out = await catalogue.send(
    new GetItemCommand({ TableName: CATALOGUE, Key: { id: { S: id } } }),
  );
  return out.Item ? JSON.parse(out.Item.item!.S!) : null;
}

export async function writeItem(id: string, item: unknown): Promise<void> {
  await catalogue.send(
    new PutItemCommand({
      TableName: CATALOGUE,
      Item: { id: { S: id }, item: { S: JSON.stringify(item) } },
    }),
  );
}

export async function countItems(): Promise<number> {
  const out = await catalogue.send(
    new ScanCommand({ TableName: CATALOGUE, ProjectionExpression: "id" }),
  );
  return (out.Items ?? []).length;
}

/**
 * The database's name rather than its address.
 *
 * Asked of the database rather than read off the client. Reaching into `pg`'s
 * own `options` works uncompiled, where the Pool was built from a connection
 * string, and returns undefined compiled, where the shim builds it from a host,
 * a port and a lazily-fetched password -- so the route answered locally and
 * threw once deployed. A query is the public API, it means the same thing in
 * both halves, and it proves the connection works while it is at it.
 *
 * The host is deliberately not reported: it is the one thing in there that
 * legitimately differs between a laptop and a deployment.
 */
export async function databaseName(): Promise<string> {
  const out = await db.query<{ name: string }>("SELECT current_database() AS name");
  return out.rows[0].name;
}
