// Key/value store backed by DynamoDB.
//
// Values are stored JSON-encoded under a single `value` attribute so that any
// JSON-shaped object round-trips unchanged. Storing native attributes instead
// would quietly reject empty strings and reorder nothing usefully.

import { DynamoDBClient, DeleteItemCommand, GetItemCommand, PutItemCommand, ScanCommand } from "@aws-sdk/client-dynamodb";

import { common, env, slug } from "./client.js";

export function connect(id) {
  const table = env(`CLOUDCC_KV_${slug(id)}_TABLE`, "persist", id);
  return new KVStore(id, table, new DynamoDBClient(common()));
}

export class KVStore {
  constructor(id, table, client) {
    this.id = id;
    this._table = table;
    this._client = client;
  }

  async get(key) {
    const out = await this._client.send(
      new GetItemCommand({ TableName: this._table, Key: { id: { S: String(key) } } }),
    );
    if (!out.Item) {
      return null;
    }
    return JSON.parse(out.Item.value.S);
  }

  async put(key, value) {
    await this._client.send(
      new PutItemCommand({
        TableName: this._table,
        Item: { id: { S: String(key) }, value: { S: JSON.stringify(value) } },
      }),
    );
  }

  async delete(key) {
    await this._client.send(
      new DeleteItemCommand({ TableName: this._table, Key: { id: { S: String(key) } } }),
    );
  }

  async keys() {
    const out = [];
    let start;
    do {
      const page = await this._client.send(
        new ScanCommand({
          TableName: this._table,
          ProjectionExpression: "id",
          ExclusiveStartKey: start,
        }),
      );
      for (const item of page.Items ?? []) {
        out.push(item.id.S);
      }
      start = page.LastEvaluatedKey;
    } while (start);
    return out.sort();
  }
}
