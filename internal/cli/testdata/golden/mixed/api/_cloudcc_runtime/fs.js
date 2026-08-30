// File store backed by S3.
//
// Same shape as kv.js and for the same reason: `connect` returns an `S3Client`,
// not a wrapper, so `GetObjectCommand`, `Upload` from `@aws-sdk/lib-storage`,
// presigned URLs and every other thing built on the AWS SDK keep working.
//
// The Python side wraps `pathlib.Path` and hands back a `cloudpathlib.S3Path`,
// which has the same API. Node has no equivalent, so the client is the object
// and the shim binds it to the provisioned bucket.

import { S3Client } from "@aws-sdk/client-s3";

import { common, env, slug } from "./client.js";
import { wrap } from "./trace.js";

export function connect(id) {
  const bucket = env(`CLOUDCC_FS_${slug(id)}_BUCKET`, "persist", id);
  const client = new S3Client(common());
  bindBucket(client, bucket);
  return wrap(client, "fs", id);
}

/**
 * Point every command this client sends at `bucket`.
 *
 * Exported for the tests, which check the rewrite without a network.
 */
export function bindBucket(client, bucket) {
  client.middlewareStack.add(
    (next) => (args) => {
      args.input = rewrite(args.input, bucket);
      return next(args);
    },
    { step: "initialize", name: "cloudccBindBucket", priority: "high" },
  );
  return client;
}

function rewrite(input, bucket) {
  if (input === null || typeof input !== "object") {
    return input;
  }
  const out = { ...input };
  if ("Bucket" in out) {
    out.Bucket = bucket;
  }
  // CopyObject names its source as "bucket/key" in a single string.
  if (typeof out.CopySource === "string") {
    const slash = out.CopySource.replace(/^\/+/, "").indexOf("/");
    if (slash > 0) {
      out.CopySource = `${bucket}/${out.CopySource.replace(/^\/+/, "").slice(slash + 1)}`;
    }
  }
  return out;
}
