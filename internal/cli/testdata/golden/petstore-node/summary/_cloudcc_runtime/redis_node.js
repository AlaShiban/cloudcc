// Cache backed by ElastiCache or MemoryDB, via node-redis.
//
// See redis_ioredis.js for why there is one module per library and why connect
// is synchronous.

import { createClient } from "redis";

import { endpoint } from "./redis_endpoint.js";

export function connect(id) {
  const { host, port, tls } = endpoint(id);
  const client = createClient({ socket: { host, port, tls } });
  // node-redis queues commands once connect() has been *called*, but throws if
  // it never was. Kicking it off here is what makes the synchronous return
  // safe; the catch keeps a failed connection from surfacing as an unhandled
  // rejection rather than on the command the caller awaits.
  client.connect().catch(() => {});
  return client;
}
