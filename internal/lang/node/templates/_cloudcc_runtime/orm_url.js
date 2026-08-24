// Where a database lives, and how its managed password is obtained.
//
// The URL delivered in the environment carries no password: AWS manages the
// master credential, and the compiler passes the managed secret's ARN
// separately so nothing sensitive ever sits in an environment variable.

import { GetSecretValueCommand, SecretsManagerClient } from "@aws-sdk/client-secrets-manager";

import { common, env, slug } from "./client.js";

/** The passwordless connection URL and the secret holding the password. */
export function target(id) {
  const key = slug(id);
  return {
    url: env(`CLOUDCC_ORM_${key}_URL`, "persist", id),
    secretArn: process.env[`CLOUDCC_ORM_${key}_SECRET_ARN`] || "",
  };
}

/**
 * The password for `id`, fetched once and cached.
 *
 * This is async, which is why the clients below are handed a password
 * *provider* rather than a password: it lets connect() stay synchronous, and
 * a synchronous connect is what keeps a compiled program's bindings the same
 * shape as an uncompiled one's.
 */
export function password(id) {
  const { secretArn } = target(id);
  let cached = null;
  return async () => {
    if (cached !== null) {
      return cached;
    }
    if (!secretArn) {
      return "";
    }
    const client = new SecretsManagerClient(common());
    const out = await client.send(new GetSecretValueCommand({ SecretId: secretArn }));
    cached = JSON.parse(out.SecretString).password;
    return cached;
  };
}

/** The URL split into the parts a driver wants, password excluded. */
export function parts(id) {
  const { url } = target(id);
  const parsed = new URL(url);
  return {
    host: parsed.hostname,
    port: Number(parsed.port || (parsed.protocol.startsWith("mysql") ? 3306 : 5432)),
    user: decodeURIComponent(parsed.username),
    database: parsed.pathname.replace(/^\//, ""),
    mysql: parsed.protocol.startsWith("mysql"),
  };
}
