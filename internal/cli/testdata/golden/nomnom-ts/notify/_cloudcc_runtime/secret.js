// Secret backed by AWS Secrets Manager.

import { GetSecretValueCommand, PutSecretValueCommand, SecretsManagerClient } from "@aws-sdk/client-secrets-manager";

import { common, env, slug } from "./client.js";
import { emit } from "./trace.js";

export function connect(id) {
  const arn = env(`CLOUDCC_SECRET_${slug(id)}_ARN`, "persist", id);
  return new Secret(id, arn, new SecretsManagerClient(common()));
}

export class Secret {
  constructor(id, arn, client) {
    this.id = id;
    this._arn = arn;
    this._client = client;
  }

  async get() {
    const out = await this._client.send(new GetSecretValueCommand({ SecretId: this._arn }));
    const value = out.SecretString ?? Buffer.from(out.SecretBinary).toString("utf8");
    // Length, never the value: a trace goes to stderr and on to CloudWatch
    // (D21). What is worth recording is that the read happened and found
    // something, which is what tells a working binding from one quietly
    // yielding "".
    emit("secret", this.id, "get", { ret: `<secret:${value.length}>` });
    return value;
  }

  async set(value) {
    emit("secret", this.id, "set", { args: { len: String(value).length } });
    await this._client.send(
      new PutSecretValueCommand({ SecretId: this._arn, SecretString: String(value) }),
    );
  }
}
