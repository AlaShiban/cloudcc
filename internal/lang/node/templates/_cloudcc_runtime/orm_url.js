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
 * How to authenticate to `id`, decided synchronously.
 *
 * Returns an object to spread into a driver's options: either `{ password }`
 * with a value or an async provider, or `{}` when there is no password to
 * supply. The distinction matters -- handing a driver an empty string is not
 * the same as saying nothing, and Postgres rejects the former outright rather
 * than falling back to PGPASSWORD or .pgpass.
 *
 * The provider form is async, which is why callers are handed a *provider*
 * rather than a password: it lets connect() stay synchronous, and a
 * synchronous connect is what keeps a compiled program's bindings the same
 * shape as an uncompiled one's.
 */
export function credentials(id) {
  const { url, secretArn } = target(id);

  if (secretArn) {
    let cached = null;
    return {
      password: async () => {
        if (cached === null) {
          const client = new SecretsManagerClient(common());
          const out = await client.send(new GetSecretValueCommand({ SecretId: secretArn }));
          cached = JSON.parse(out.SecretString).password;
        }
        return cached;
      },
    };
  }

  // No managed secret: the URL may carry the credential itself, and if it does
  // not, saying nothing lets the driver use its own resolution.
  const inUrl = decodeURIComponent(new URL(url).password || "");
  return inUrl === "" ? {} : { password: inUrl };
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
