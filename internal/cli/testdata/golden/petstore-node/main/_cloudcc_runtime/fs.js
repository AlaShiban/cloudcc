// File store backed by S3.

import { DeleteObjectCommand, GetObjectCommand, HeadObjectCommand, ListObjectsV2Command, PutObjectCommand, S3Client } from "@aws-sdk/client-s3";

import { common, env, slug } from "./client.js";

export function connect(id) {
  const bucket = env(`CLOUDCC_FS_${slug(id)}_BUCKET`, "persist", id);
  return new FileStore(id, bucket, new S3Client(common()));
}

export class FileStore {
  constructor(id, bucket, client) {
    this.id = id;
    this._bucket = bucket;
    this._client = client;
  }

  _key(key) {
    return String(key).replace(/^\/+/, "");
  }

  async read(key) {
    try {
      const out = await this._client.send(
        new GetObjectCommand({ Bucket: this._bucket, Key: this._key(key) }),
      );
      return Buffer.from(await out.Body.transformToByteArray());
    } catch (err) {
      // Match the SDK's local emulation, which throws rather than leaking the
      // provider's own error shape to callers.
      if (err?.name === "NoSuchKey" || err?.$metadata?.httpStatusCode === 404) {
        const e = new Error(`no such key: ${key}`);
        e.code = "ENOENT";
        throw e;
      }
      throw err;
    }
  }

  async write(key, data) {
    await this._client.send(
      new PutObjectCommand({ Bucket: this._bucket, Key: this._key(key), Body: data }),
    );
  }

  async delete(key) {
    await this._client.send(
      new DeleteObjectCommand({ Bucket: this._bucket, Key: this._key(key) }),
    );
  }

  async exists(key) {
    try {
      await this._client.send(
        new HeadObjectCommand({ Bucket: this._bucket, Key: this._key(key) }),
      );
      return true;
    } catch {
      return false;
    }
  }

  async list(prefix = "") {
    const out = [];
    let token;
    do {
      const page = await this._client.send(
        new ListObjectsV2Command({ Bucket: this._bucket, Prefix: prefix, ContinuationToken: token }),
      );
      for (const obj of page.Contents ?? []) {
        out.push(obj.Key);
      }
      token = page.IsTruncated ? page.NextContinuationToken : undefined;
    } while (token);
    return out.sort();
  }
}
