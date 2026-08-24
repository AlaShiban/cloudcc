/**
 * Local emulations behind the SDK hints.
 *
 * These exist for one reason: so that a program written against the SDK still
 * runs with `node server.js` on a laptop, with no cloud account. They are
 * deliberately small. A KV store is a Map; a bucket is a directory.
 *
 * Their method signatures are the contract the injected `_cloudcc_runtime`
 * clients must match exactly, and a parity test compares the two -- because
 * two implementations of one API drift otherwise.
 *
 * Every method that reaches a store is asynchronous, even though a Map lookup
 * needs no await. That is deliberate: the injected client talks to AWS and
 * cannot be synchronous, so if these were synchronous, compiling a program
 * would silently change what it does -- `pets.get(id)` would go from returning
 * a value to returning a promise. Matching the shape here is what makes the
 * compile behaviour-preserving.
 */

import { existsSync, mkdirSync, readdirSync, readFileSync, rmSync, statSync, unlinkSync, writeFileSync } from "node:fs";
import { dirname, join, posix, relative, resolve } from "node:path";

/** Where directory-backed emulations keep their state. */
export const LOCAL_STATE_DIR_ENV = "CLOUDCC_LOCAL_STATE_DIR";
const DEFAULT_LOCAL_ROOT = ".cloudcc-local";

/** The directory local emulations write to. */
export function localRoot(): string {
  return process.env[LOCAL_STATE_DIR_ENV] ?? DEFAULT_LOCAL_ROOT;
}

const registries: Array<Map<string, unknown>> = [];

function registry<T>(): Map<string, T> {
  const m = new Map<string, T>();
  registries.push(m as Map<string, unknown>);
  return m;
}

/** Delete every local emulation's state. Intended for tests. */
export function resetLocalState(): void {
  const root = localRoot();
  if (existsSync(root)) {
    rmSync(root, { recursive: true, force: true });
  }
  for (const r of registries) {
    r.clear();
  }
}

/** A JSON-shaped value, which is what these stores hold. */
export type Json = Record<string, unknown>;

/** A key/value store keyed by string, holding JSON-shaped values. */
export class KVStore {
  readonly id: string;
  readonly #items = new Map<string, string>();

  constructor(id: string) {
    this.id = id;
  }

  /** Return the item at `key`, or null. */
  async get(key: string): Promise<Json | null> {
    const raw = this.#items.get(String(key));
    return raw === undefined ? null : (JSON.parse(raw) as Json);
  }

  /** Store `value` at `key`. */
  async put(key: string, value: Json): Promise<void> {
    this.#items.set(String(key), JSON.stringify(value));
  }

  /** Remove `key` if present. */
  async delete(key: string): Promise<void> {
    this.#items.delete(String(key));
  }

  /** Every key currently stored, sorted. */
  async keys(): Promise<string[]> {
    return [...this.#items.keys()].sort();
  }
}

/** A file store backed by a local directory. */
export class Bucket {
  readonly id: string;

  constructor(id: string) {
    this.id = id;
  }

  #path(key: string): string {
    const safe = String(key).replace(/^\/+/, "");
    return join(localRoot(), "fs", this.id, safe);
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
    const base = join(localRoot(), "fs", this.id);
    if (!existsSync(base)) {
      return [];
    }
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
    walk(resolve(base));
    return out.filter((k) => k.startsWith(prefix)).sort();
  }
}

/** A single secret value. */
export class Secret {
  readonly id: string;
  #value: string | null = null;

  constructor(id: string) {
    this.id = id;
  }

  /**
   * Return the secret's value. Locally this reads `CLOUDCC_SECRET_<ID>` from
   * the environment, so a test can supply one without a cloud account.
   */
  async get(): Promise<string> {
    if (this.#value !== null) {
      return this.#value;
    }
    return process.env[`CLOUDCC_SECRET_${slug(this.id)}`] ?? "";
  }

  /** Replace the secret's value. */
  async set(value: string): Promise<void> {
    this.#value = String(value);
  }
}

/**
 * A relational database handle.
 *
 * The emulation offers the connection URL rather than a client, so the same
 * call works with whichever driver the program already uses.
 */
export class OrmSession {
  readonly id: string;

  constructor(id: string) {
    this.id = id;
  }

  /** The database connection URL. */
  async url(): Promise<string> {
    const root = join(localRoot(), "orm");
    mkdirSync(root, { recursive: true });
    return `sqlite://${join(root, `${this.id}.db`)}`;
  }
}

/** A Redis-compatible cache. */
export class Redis {
  readonly id: string;
  readonly #items = new Map<string, string>();

  constructor(id: string) {
    this.id = id;
  }

  /** Return the value at `key`, or null. */
  async get(key: string): Promise<string | null> {
    return this.#items.get(String(key)) ?? null;
  }

  /**
   * Store `value` at `key`, optionally expiring after `ex` seconds. The local
   * emulation ignores `ex`: nothing here is long-lived enough for expiry to be
   * observable.
   */
  async set(key: string, value: string, ex?: number): Promise<void> {
    this.#items.set(String(key), String(value));
  }

  /** Remove `key` if present. */
  async delete(key: string): Promise<void> {
    this.#items.delete(String(key));
  }

  /** Increment `key` and return the new value. */
  async incr(key: string, amount = 1): Promise<number> {
    const next = Number(this.#items.get(String(key)) ?? "0") + amount;
    this.#items.set(String(key), String(next));
    return next;
  }
}

/** A handler a topic delivers messages to. */
export type Subscriber = (message: Json) => unknown;

/** A publish/subscribe topic with in-process fan-out. */
export class Topic {
  readonly id: string;
  readonly #subscribers: Subscriber[] = [];

  constructor(id: string) {
    this.id = id;
  }

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

export const kvStores = registry<KVStore>();
export const fileStores = registry<Bucket>();
export const secrets = registry<Secret>();
export const databases = registry<OrmSession>();
export const caches = registry<Redis>();
export const topics = registry<Topic>();
