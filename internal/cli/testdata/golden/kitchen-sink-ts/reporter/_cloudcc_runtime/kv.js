// Key/value store backed by DynamoDB.
//
// There is no class here, and that is the design. `connect` returns the same
// object the uncompiled program held -- a `DynamoDBClient` -- so every command
// the AWS SDK has, and every one it gains, works because it *is* the AWS SDK.
// A class of ours would have supported the four methods someone thought of, in
// a dialect nobody else speaks, and would have had to be kept in step with the
// SDK's local emulation forever.
//
// Node has no boto3, so unlike Python's `Table` this client is not bound to a
// single table: the name travels in each command. The shim binds it, with a
// middleware that rewrites whatever table name the program wrote to the one the
// compiler provisioned.
//
// Rewriting *any* name rather than matching a convention is deliberate. This
// client was declared as one store, so every command it sends belongs to that
// store; and it means the uncompiled program can call its local table whatever
// it likes without the two runs having to agree on a string.

import { DynamoDBClient } from "@aws-sdk/client-dynamodb";

import { common, env, slug } from "./client.js";
import { wrap } from "./trace.js";

export function connect(id) {
  const table = env(`CLOUDCC_KV_${slug(id)}_TABLE`, "persist", id);
  const client = new DynamoDBClient(common());
  bindTable(client, table);
  return wrap(client, "kv", id);
}

/**
 * Point every command this client sends at `table`.
 *
 * Exported for the tests, which check the rewrite without a network.
 */
export function bindTable(client, table) {
  client.middlewareStack.add(
    (next) => (args) => {
      args.input = rewrite(args.input, table);
      return next(args);
    },
    { step: "initialize", name: "cloudccBindTable", priority: "high" },
  );
  return client;
}

function rewrite(input, table) {
  if (input === null || typeof input !== "object") {
    return input;
  }
  const out = { ...input };

  // The common shape: GetItem, PutItem, Query, Scan, UpdateItem, DeleteItem.
  if ("TableName" in out) {
    out.TableName = table;
  }

  // BatchGetItem and BatchWriteItem key their requests by table name, so the
  // name is a property rather than a value.
  if (out.RequestItems && typeof out.RequestItems === "object") {
    const merged = {};
    for (const value of Object.values(out.RequestItems)) {
      merged[table] = Array.isArray(value) && Array.isArray(merged[table])
        ? merged[table].concat(value)
        : value;
    }
    out.RequestItems = merged;
  }

  // TransactGetItems and TransactWriteItems nest one level further.
  if (Array.isArray(out.TransactItems)) {
    out.TransactItems = out.TransactItems.map((item) => {
      const copy = { ...item };
      for (const verb of ["Get", "Put", "Update", "Delete", "ConditionCheck"]) {
        if (copy[verb]) {
          copy[verb] = { ...copy[verb], TableName: table };
        }
      }
      return copy;
    });
  }

  return out;
}
