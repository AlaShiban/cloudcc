// Cache backed by ElastiCache or MemoryDB, via node-redis.
//
// See redis_ioredis.js for why there is one module per library and why connect
// is synchronous.

import { createClient } from "redis";

import { endpoint } from "./redis_endpoint.js";
import { wrap } from "./trace.js";

export function connect(id) {
  const { host, port, tls } = endpoint(id);
  const client = createClient({ socket: { host, port, tls } });
  // node-redis queues commands once connect() has been *called*, but throws if
  // it never was. Kicking it off here is what makes the synchronous return
  // safe; the catch keeps a failed connection from surfacing as an unhandled
  // rejection rather than on the command the caller awaits.
  const opening = client.connect().catch(() => {});

  // A program that connects for itself -- the ordinary way to use node-redis,
  // and what the same source does before it is compiled -- must not be told
  // "Socket already opened" just because this shim got there first. Handing
  // back the connection already in flight makes that second call behave as the
  // only call did locally, and resolve to the client as node-redis does.
  //
  // Found by the seam trace: `connect` recorded `ret=<Commander>` uncompiled
  // and `err=Error` compiled, while both halves answered every request
  // identically -- the example happens to write `.catch(() => undefined)`
  // around it. Without that guard the program works locally and throws once
  // deployed.
  client.connect = () => opening.then(() => client);

  return wrap(client, "redis", id);
}
