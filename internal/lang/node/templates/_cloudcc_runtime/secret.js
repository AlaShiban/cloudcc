// Secret backed by AWS Secrets Manager.

import { GetSecretValueCommand, PutSecretValueCommand, SecretsManagerClient } from "@aws-sdk/client-secrets-manager";

import { common, env, slug } from "./client.js";

export function connect(id) {
  const arn = env(`CLOUDCC_SECRET_${slug(id)}_ARN`, "persistSecret", id);
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
    return out.SecretString ?? Buffer.from(out.SecretBinary).toString("utf8");
  }

  async set(value) {
    await this._client.send(
      new PutSecretValueCommand({ SecretId: this._arn, SecretString: String(value) }),
    );
  }
}
