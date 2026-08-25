/**
 * The typed clients this package supplies.
 *
 * **No data stores.** Every store has a client library already -- `ioredis`,
 * `pg`, `@aws-sdk/client-dynamodb`, `@aws-sdk/client-s3` -- and `persist` wraps
 * those untouched. A class of ours would have its own method names, and those
 * would have to be kept in step with the injected runtime's forever; worse, it
 * would be a dialect nobody else speaks, so code written against it could not
 * be lifted back out.
 *
 * There used to be a `KVStore` and a `FileStore` here. Both are gone. Node has
 * no pathlib and no boto3, so where the Python SDK wraps a `Table` or a `Path`,
 * a Node program wraps the AWS SDK client for the same service -- and the
 * injected shim hands back a client of that same type, with a middleware that
 * rewrites the logical resource name the program wrote to the physical one the
 * compiler chose.
 *
 * What is left are the two capabilities that are not stores. A pub/sub topic is
 * a decision about how messages move, and a secret is a value the environment
 * holds; neither has a client to wrap.
 *
 * Every method that reaches one of them is asynchronous, even where the local
 * implementation needs no await. That is deliberate: the injected client talks
 * to AWS and cannot be synchronous, so if these were synchronous, compiling a
 * program would silently change what it does.
 *
 * Their method signatures are the contract the injected `_cloudcc_runtime`
 * clients must match exactly, and a parity test compares the two.
 */

import {
  existsSync,
  mkdirSync,
  readdirSync,
  readFileSync,
  rmSync,
  statSync,
  unlinkSync,
  writeFileSync,
} from "node:fs";
import { dirname, join, posix, relative, resolve } from "node:path";

/** Where the supplied clients keep their state. */
export const LOCAL_STATE_DIR_ENV = "CLOUDCC_LOCAL_STATE_DIR";
const DEFAULT_LOCAL_ROOT = ".cloudcc-local";

/** The directory the supplied clients write to. */
export function localRoot(): string {
  return process.env[LOCAL_STATE_DIR_ENV] ?? DEFAULT_LOCAL_ROOT;
}

/** Delete every supplied client's local state. Intended for tests. */
export function resetLocalState(): void {
  const root = localRoot();
  if (existsSync(root)) {
    rmSync(root, { recursive: true, force: true });
  }
}

/**
 * Each instance gets its own corner of the state directory. The id belongs to
 * `persist`, not to the constructor, so a client has to name itself somehow;
 * a counter is enough, and unlike an object identity it is stable across runs.
 */
let instances = 0;
function instanceKey(): string {
  instances += 1;
  return String(instances);
}

/** A JSON-shaped value, which is what these stores hold. */
export type Json = Record<string, unknown>;

/**
 * A single secret value. Compiles to Secrets Manager.
 *
 * Locally it reads the named environment variable, so a test can supply one
 * without a cloud account.
 */
export class Secret {
  readonly #env: string | null;
  #value: string | null = null;

  constructor(env?: string) {
    this.#env = env ?? null;
  }

  /** Return the secret's value. */
  async get(): Promise<string> {
    if (this.#value !== null) {
      return this.#value;
    }
    return (this.#env === null ? "" : process.env[this.#env]) ?? "";
  }

  /** Replace the secret's value. */
  async set(value: string): Promise<void> {
    this.#value = String(value);
  }
}

/** A handler a topic delivers messages to. */
export type Subscriber = (message: Json) => unknown;

/**
 * A publish/subscribe topic with in-process fan-out. Compiles to SNS.
 *
 * Locally a publisher and a subscriber in the same program behave as they will
 * once they are separate Lambdas.
 */
/**
 * What a topic must guarantee. The compiler picks the service that meets it.
 *
 * This is the inversion that makes pub/sub different from storage: for a store
 * the library picks the capability and cloudcc.yaml picks between variants that
 * behave alike, but here the variants do not behave alike -- SNS cannot replay,
 * SQS cannot fan out, and FIFO everything costs throughput. Choosing by hand
 * means knowing all of that; declaring the requirement means the compiler has
 * to. A set no service can meet is a compile error naming what to relax.
 */
export interface TopicRequirements {
  /** "many" fans out; "one" is a queue with a single consumer. */
  subscribers?: "many" | "one";
  /** "none", "key" (ordered within a key), or "total" (globally ordered). */
  ordering?: "none" | "key" | "total";
  delivery?: "at_least_once" | "exactly_once";
  /** Whether a new subscriber can read messages sent before it existed. */
  replay?: boolean;
  retentionHours?: number;
  maxMessageKb?: number;
}

export class Topic {
  readonly #subscribers: Subscriber[] = [];

  /**
   * The requirements are read by the compiler, not by this class. Locally
   * dispatch is in order and exactly once whatever they say -- the usual
   * bargain with an emulation: the code path is exercised, the timing is not.
   */
  constructor(readonly requirements: TopicRequirements = {}) {}

  /** Deliver `message` to every subscriber. */
  async publish(message: Json): Promise<void> {
    for (const fn of [...this.#subscribers]) {
      fn(message);
    }
  }

  /** Register `fn` as a subscriber; returns it, so it reads as a wrapper. */
  subscribe(fn: Subscriber): Subscriber {
    this.#subscribers.push(fn);
    return fn;
  }

  /** The registered subscribers, in registration order. */
  subscribers(): readonly Subscriber[] {
    return [...this.#subscribers];
  }
}

/**
 * An inert handle returned by `expose`. It exists so the call has a value
 * worth binding and so editors can show what was exposed; it has no runtime
 * behaviour of its own.
 */
export class Gateway {
  readonly id: string;
  readonly target: string;
  readonly app: unknown;

  constructor(id: string, target = "public", app: unknown = null) {
    this.id = id;
    this.target = target;
    this.app = app;
  }

  /**
   * The deployed URL, delivered by the compiler as an environment variable.
   * Empty when running locally.
   */
  url(): string {
    return process.env[`CLOUDCC_GATEWAY_${slug(this.id)}_URL`] ?? "";
  }
}

/**
 * The environment-variable spelling of a capability id. Must agree with
 * sanitize.EnvVar in the compiler and with the identical function in the
 * injected runtime; a parity test pins all three together.
 */
export function slug(id: string): string {
  return [...id].map((c) => (/[a-zA-Z0-9]/.test(c) ? c.toUpperCase() : "_")).join("");
}
