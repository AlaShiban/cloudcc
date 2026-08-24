/**
 * The typed clients this package supplies.
 *
 * Most capabilities have a standard client already -- `ioredis`, `pg`,
 * `sequelize` -- and `persist` wraps those untouched. A few do not: a key/value
 * store, a pub/sub topic, a secret and a plain file store have no library
 * everyone reaches for. Those get a class here, so every capability is declared
 * the same way.
 *
 * These are real local implementations rather than mocks. A key/value store is
 * a JSON file, because `persist` promising persistence and handing back a Map
 * that vanishes on exit would be a poor joke.
 *
 * Every method that reaches a store is asynchronous, even though a file read
 * needs no await. That is deliberate: the injected client talks to AWS and
 * cannot be synchronous, so if these were synchronous, compiling a program
 * would silently change what it does -- `pets.get(id)` would go from returning
 * a value to returning a promise. Matching the shape here is what makes the
 * compile behaviour-preserving.
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
 * A key/value store keyed by string, holding JSON-shaped values. Compiles to
 * DynamoDB.
 *
 * The id is supplied by `persist`, not by the constructor, so the object reads
 * as a plain client until it is wrapped.
 */
export class KVStore {
  readonly #path: string;

  constructor(path?: string) {
    this.#path = path ?? join(localRoot(), "kv", `${instanceKey()}.json`);
  }

  #read(): Record<string, Json> {
    if (!existsSync(this.#path)) {
      return {};
    }
    const raw = readFileSync(this.#path, "utf8");
    return raw === "" ? {} : (JSON.parse(raw) as Record<string, Json>);
  }

  #write(items: Record<string, Json>): void {
    mkdirSync(dirname(this.#path), { recursive: true });
    const sorted: Record<string, Json> = {};
    for (const key of Object.keys(items).sort()) {
      sorted[key] = items[key];
    }
    writeFileSync(this.#path, JSON.stringify(sorted));
  }

  /** Return the item at `key`, or null. */
  async get(key: string): Promise<Json | null> {
    return this.#read()[String(key)] ?? null;
  }

  /** Store `value` at `key`. */
  async put(key: string, value: Json): Promise<void> {
    const items = this.#read();
    items[String(key)] = value;
    this.#write(items);
  }

  /** Remove `key` if present. */
  async delete(key: string): Promise<void> {
    const items = this.#read();
    delete items[String(key)];
    this.#write(items);
  }

  /** Every key currently stored, sorted. */
  async keys(): Promise<string[]> {
    return Object.keys(this.#read()).sort();
  }
}

/**
 * A file store backed by a local directory. Compiles to S3.
 *
 * Pass a directory when the program already has one; otherwise it lives under
 * the state directory.
 */
export class FileStore {
  readonly #base: string;

  constructor(root?: string) {
    this.#base = root ?? join(localRoot(), "fs", instanceKey());
  }

  #path(key: string): string {
    return join(this.#base, String(key).replace(/^\/+/, ""));
  }

  /** Return the bytes stored at `key`, throwing when absent. */
  async read(key: string): Promise<Buffer> {
    return readFileSync(this.#path(key));
  }

  /** Store `data` at `key`. */
  async write(key: string, data: Buffer | string): Promise<void> {
    const path = this.#path(key);
    mkdirSync(dirname(path), { recursive: true });
    writeFileSync(path, data);
  }

  /** Remove `key` if present. */
  async delete(key: string): Promise<void> {
    const path = this.#path(key);
    if (existsSync(path)) {
      unlinkSync(path);
    }
  }

  /** Whether `key` is present. */
  async exists(key: string): Promise<boolean> {
    const path = this.#path(key);
    return existsSync(path) && statSync(path).isFile();
  }

  /** Every key under `prefix`, sorted. */
  async list(prefix = ""): Promise<string[]> {
    if (!existsSync(this.#base)) {
      return [];
    }
    const base = resolve(this.#base);
    const out: string[] = [];
    const walk = (dir: string): void => {
      for (const entry of readdirSync(dir, { withFileTypes: true })) {
        const full = join(dir, entry.name);
        if (entry.isDirectory()) {
          walk(full);
        } else {
          out.push(posix.normalize(relative(base, full).split(/[\\/]/).join("/")));
        }
      }
    };
    walk(base);
    return out.filter((k) => k.startsWith(prefix)).sort();
  }
}

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
export class Topic {
  readonly #subscribers: Subscriber[] = [];

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
