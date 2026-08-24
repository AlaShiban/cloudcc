/**
 * CloudCompiler SDK: compile-time hints for a plain Node application.
 *
 * Every exported function here is a *hint*. The compiler reads these calls
 * statically -- it never imports or executes this package -- and rewrites
 * them, in a copy of your source, into real cloud clients.
 *
 * Two rules follow from that:
 *
 * - Arguments must be literals. `persistKv("pets")` is fine;
 *   `persistKv(name)` is a compile error with a precise source location,
 *   because the compiler would have to run your program to know the value.
 * - Calls belong at module top level, where the compiler can see the shape of
 *   the program. `executionUnit` in particular must be a top-level call.
 *
 * At runtime, outside the compiler, these functions return small local
 * emulations -- a Map for a KV store, a directory for a bucket -- so
 * `node server.js` still runs on your laptop with no cloud account and no
 * credentials. The emulations are deliberately minimal; they exist so the
 * program runs, not so it behaves identically to AWS.
 *
 * This package never imports the AWS SDK. Cloud access only ever appears in
 * the `_cloudcc_runtime` package the compiler injects into the compiled copy.
 */

import {
  Bucket,
  Gateway,
  Json,
  KVStore,
  OrmSession,
  Redis,
  Secret,
  Topic,
  caches,
  databases,
  fileStores,
  kvStores,
  secrets,
  slug,
  topics,
} from "./emulation.js";

export {
  Bucket,
  Gateway,
  KVStore,
  OrmSession,
  Redis,
  Secret,
  Topic,
  localRoot,
  resetLocalState,
  slug,
  LOCAL_STATE_DIR_ENV,
} from "./emulation.js";
export type { Json, Subscriber } from "./emulation.js";

/**
 * Mark this module as the entrypoint of an execution unit.
 *
 * The unit's contents are the transitive local-import closure of this module.
 * A program with no `executionUnit` call at all is compiled as a single unit
 * named `main`.
 *
 * `type` is a weak hint ("lambda", "ecs"); cloudcc.yaml overrides it.
 */
export function executionUnit(options: { id: string; type?: string }): void {
  void options;
}

/**
 * Expose an application to the network.
 *
 * `app` is the application object itself -- the one argument that is an
 * expression rather than a literal, because the compiler only needs to know
 * *which binding* holds it.
 */
export function expose(app: unknown, options: { id?: string; target?: string } = {}): Gateway {
  return new Gateway(options.id ?? "main", options.target ?? "public", app);
}

/** A key/value store. Compiles to DynamoDB. */
export function persistKv(id: string): KVStore {
  return memo(kvStores, id, () => new KVStore(id));
}

/** A file store. Compiles to S3. */
export function persistFs(id: string): Bucket {
  return memo(fileStores, id, () => new Bucket(id));
}

/** A secret. Compiles to Secrets Manager. */
export function persistSecret(id: string): Secret {
  return memo(secrets, id, () => new Secret(id));
}

/** A relational database. Compiles to RDS Postgres. */
export function persistOrm(id: string, options: { models?: string[] } = {}): OrmSession {
  void options;
  return memo(databases, id, () => new OrmSession(id));
}

/** A Redis-compatible cache. Compiles to ElastiCache or MemoryDB. */
export function persistRedis(id: string): Redis {
  return memo(caches, id, () => new Redis(id));
}

/** A publish/subscribe topic. Compiles to SNS. */
export function pubsubTopic(id: string): Topic {
  return memo(topics, id, () => new Topic(id));
}

/**
 * A runtime configuration value, delivered as an environment variable.
 *
 * `secret: true` makes the compiled project store the value as a Pulumi stack
 * secret rather than as plaintext.
 */
export function configValue(id: string, options: { default?: string; secret?: boolean } = {}): string {
  return process.env[`CLOUDCC_CONFIG_${slug(id)}`] ?? options.default ?? "";
}

/**
 * Serve a bundle of static files from object storage.
 *
 * `staticFiles` is claimed out of the source pool before execution units are
 * assembled, so those assets never end up inside a compute bundle.
 * `sharedFiles` are uploaded too but stay importable by your code.
 */
export function staticUnit(
  id: string,
  options: { staticFiles: string; indexDocument?: string; sharedFiles?: string },
): void {
  void id;
  void options;
}

/**
 * Claim files matching `pattern` so they travel with the execution unit.
 * Returns the pattern unchanged, so it can be used inline where a path is
 * expected.
 */
export function embedAssets(pattern: string): string {
  return pattern;
}

function memo<T>(store: Map<string, T>, id: string, make: () => T): T {
  const existing = store.get(id);
  if (existing !== undefined) {
    return existing;
  }
  const created = make();
  store.set(id, created);
  return created;
}

export type { Json as JsonValue };
