// Cache backed by ElastiCache or MemoryDB, over the Redis protocol.

import { createClient } from "redis";

import { env, slug } from "./client.js";

export async function connect(id) {
  const key = slug(id);
  const host = env(`CLOUDCC_REDIS_${key}_ENDPOINT`, "persistRedis", id);
  const port = Number(env(`CLOUDCC_REDIS_${key}_PORT`, "persistRedis", id));
  const tls = process.env[`CLOUDCC_REDIS_${key}_TLS`] === "true";

  const client = createClient({ socket: { host, port, tls } });
  await client.connect();
  return new Redis(id, client);
}

export class Redis {
  constructor(id, client) {
    this.id = id;
    this._client = client;
  }

  async get(key) {
    return (await this._client.get(String(key))) ?? null;
  }

  async set(key, value, ex) {
    if (ex === undefined) {
      await this._client.set(String(key), String(value));
      return;
    }
    await this._client.set(String(key), String(value), { EX: ex });
  }

  async delete(key) {
    await this._client.del(String(key));
  }

  async incr(key, amount = 1) {
    return Number(await this._client.incrBy(String(key), amount));
  }
}
