// Cache backed by ElastiCache or MemoryDB, via ioredis.
//
// One module per client library, each importing exactly one package. The
// compiler points a program at the module matching the client it declared, so
// a bundle carries ioredis or node-redis but never both.
//
// connect is synchronous on purpose. Uncompiled, `persist(new Redis(), ...)`
// hands back a client immediately; if this returned a promise the same
// expression would be a client before compiling and a Promise after, and
// `cache.get(k)` would stop working. ioredis connects lazily and queues
// commands until the socket is ready, so there is nothing to await.

import IORedis from "ioredis";

import { endpoint } from "./redis_endpoint.js";

export function connect(id) {
  const { host, port, tls } = endpoint(id);
  return new IORedis({ host, port, ...(tls ? { tls: {} } : {}) });
}
