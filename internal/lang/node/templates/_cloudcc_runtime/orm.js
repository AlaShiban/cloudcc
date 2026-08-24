// Relational database handle backed by RDS.

import { GetSecretValueCommand, SecretsManagerClient } from "@aws-sdk/client-secrets-manager";

import { common, env, slug } from "./client.js";

export function connect(id) {
  const key = slug(id);
  const url = env(`CLOUDCC_ORM_${key}_URL`, "persist", id);
  return new OrmSession(id, url, process.env[`CLOUDCC_ORM_${key}_SECRET_ARN`]);
}

export class OrmSession {
  constructor(id, url, secretArn) {
    this.id = id;
    this._url = url;
    this._secretArn = secretArn;
    this._resolved = null;
  }

  /**
   * The connection URL, with the managed password spliced in.
   *
   * The URL delivered in the environment carries no password: AWS manages the
   * master credential, and the compiler passes the managed secret's ARN
   * separately so nothing sensitive ever sits in an environment variable.
   */
  async url() {
    if (this._resolved !== null) {
      return this._resolved;
    }
    if (!this._secretArn) {
      this._resolved = this._url;
      return this._resolved;
    }
    const client = new SecretsManagerClient(common());
    const out = await client.send(new GetSecretValueCommand({ SecretId: this._secretArn }));
    const password = JSON.parse(out.SecretString).password;
    const [scheme, rest] = this._url.split("://");
    const [user, host] = rest.split("@");
    this._resolved = `${scheme}://${user}:${encodeURIComponent(password)}@${host}`;
    return this._resolved;
  }
}
