// Where a cache lives, read from the environment the compiler bound.

import { env, slug } from "./client.js";

export function endpoint(id) {
  const key = slug(id);
  return {
    host: env(`CLOUDCC_REDIS_${key}_ENDPOINT`, "persist", id),
    port: Number(env(`CLOUDCC_REDIS_${key}_PORT`, "persist", id)),
    tls: process.env[`CLOUDCC_REDIS_${key}_TLS`] === "true",
  };
}
