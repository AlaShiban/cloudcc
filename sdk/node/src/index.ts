/**
 * CloudCompiler SDK: compile-time hints for a plain Node application.
 *
 * Every exported function here is a *hint*. The compiler reads these calls
 * statically -- it never imports or executes this package -- and rewrites
 * them, in a copy of your source, into real cloud clients.
 *
 * The central idea is that you bring your own client and the compiler wraps it:
 *
 * ```ts
 * const cache = persist(new Redis(), { id: "itemCache" });
 * const db    = persist(new Pool({ connectionString: "postgres://…" }), { id: "shopdb" });
 * ```
 *
 * `persist` is type-preserving: it returns exactly what you gave it, so the
 * type your editor sees is the type you keep. Uncompiled, it *is* the object
 * you passed -- your program talks to a local Redis, a local Postgres.
 * Compiled, the same expression becomes a client of the same type pointed at
 * ElastiCache or RDS.
 *
 * That is the whole point of the design. There is no parallel API to learn and
 * none for us to keep in step with yours: what you hold is always the
 * library's own type.
 *
 * Two rules follow from the hints being read rather than run:
 *
 * - Arguments must be literals. `persist(client, { id: "pets" })` is fine;
 *   `persist(client, { id: name })` is a compile error with a precise source
 *   location, because the compiler would have to run your program to know the
 *   value.
 * - Calls belong at module top level, where the compiler can see the shape of
 *   the program. `executionUnit` in particular must be a top-level call.
 *
 * This package supplies no client for a data store. A store is declared by
 * wrapping the library you already use -- `ioredis`, `pg`, a
 * `DynamoDBClient`, an `S3Client` -- because a class of ours would be a
 * dialect nobody else speaks, and its methods would have to be kept in step
 * with the injected runtime's forever. The two things it does supply, a
 * pub/sub topic and a secret, are not stores: neither has a client to wrap.
 *
 * This package never imports the AWS SDK. Cloud access only ever appears in
 * the `_cloudcc_runtime` package the compiler injects into the compiled copy.
 */

import { Gateway, Json, slug } from "./emulation.js";

export {
  Gateway,
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

/**
 * Make a client's data outlive the process.
 *
 * Pass the client you already use. The compiler reads its type to decide what
 * to provision:
 *
 * | what you pass                      | what it becomes           |
 * | ---------------------------------- | ------------------------- |
 * | `new Redis(...)` (ioredis)         | ElastiCache (or MemoryDB) |
 * | `createClient(...)` (node-redis)   | ElastiCache (or MemoryDB) |
 * | `new Pool({ … })` (pg)             | RDS Postgres              |
 * | `knex({ … })`                      | RDS Postgres or MySQL     |
 * | `new DynamoDBClient({})`           | DynamoDB                  |
 * | `new S3Client({})`                 | S3                        |
 * | `new Topic()`                      | SNS, SQS or Kinesis       |
 * | `new Secret()`                     | Secrets Manager           |
 *
 * The library you reached for supplies the default; cloudcc.yaml still chooses
 * between variants of it, so asking for MemoryDB instead of ElastiCache is a
 * configuration change rather than a code change.
 *
 * `id` names the resource and is required. It is deliberately not taken from
 * the binding you assign to: renaming a local would otherwise replace a live
 * resource, and losing a database to a rename is not a trade worth making for
 * brevity.
 *
 * Returns `client`, unchanged.
 */
export function persist<T>(client: T, options: { id: string; models?: string[] }): T {
  void options;
  return client;
}

/**
 * Call another execution unit's functions over the wire.
 *
 * `target` is the module the other unit is built around, imported the ordinary
 * way, and `id` is that unit's `executionUnit` id:
 *
 * ```js
 * import * as pricingModule from "./pricing.js";
 *
 * const pricing = remote(pricingModule, { id: "pricing" });
 * const quote = await pricing.quoteBasket(items);
 * ```
 *
 * Uncompiled this returns the module unchanged, so the await is an ordinary
 * in-process call and the program runs as one process. Compiled, the same
 * expression becomes a client of the deployed unit and the await is a request.
 *
 * Three things follow from the call becoming a request, and the compiler
 * checks all three rather than letting them surface in production:
 *
 * - **The functions must be `async`.** A remote call is a network round trip,
 *   and a signature that hides one behind a synchronous call cannot be
 *   corrected later without changing every caller.
 * - **The names must exist.** A misspelled call is a compile error listing the
 *   functions the unit does offer.
 * - **The calls may not form a cycle.** Two units awaiting each other deadlock
 *   until both time out. If units need to talk both ways, one direction is a
 *   `Topic`.
 *
 * Arguments and return values cross the wire as JSON, in the same envelope the
 * Python runtime uses -- so a unit does not need to know what the unit it is
 * calling was written in.
 *
 * Returns `target`, unchanged.
 */
export function remote<T>(target: T, options: { id: string }): T {
  void options;
  return target;
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

export type { Json as JsonValue };
