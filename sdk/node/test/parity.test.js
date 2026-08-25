/**
 * Where the SDK supplies a client, that client and the injected
 * _cloudcc_runtime one are two implementations of a single API, and they will
 * drift unless something compares them.
 *
 * This asserts, method by method, that every public method on an SDK class
 * exists on its runtime counterpart with the same arity. Python has had this
 * check since the beginning; Node did not, and the classes here carry exactly
 * the same risk.
 *
 * Most capabilities are deliberately absent from the pairs below, and that is
 * the design rather than a gap. A program that hands `persist` an ioredis
 * client or a pg Pool gets one of the same type back once compiled, so there
 * is no second implementation to keep in step. Only the capabilities with no
 * standard client need a class here, and only those can drift.
 */

import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

import * as cloudcc from "../dist/index.js";

const here = fileURLToPath(new URL(".", import.meta.url));
const SHIM_DIR = join(here, "..", "..", "..", "internal", "lang", "node", "templates", "_cloudcc_runtime");

// This suite reaches out of the package and into the compiler's template tree,
// so it only runs from a checkout. Saying so beats a bare ENOENT from the first
// readFileSync, which reads like a broken test rather than a missing tree.
test("the compiler's shim templates are reachable", () => {
  assert.ok(
    existsSync(SHIM_DIR),
    `${SHIM_DIR} not found. The parity suite compares this package against the ` +
      `compiler's injected shims, so it has to run from a cloudcc checkout.`,
  );
});

/** SDK class -> [shim module, shim class]. Named alike on purpose; the mapping
 *  is explicit anyway so a rename cannot pass silently. */
const PAIRS = [
  [cloudcc.KVStore, "kv", "KVStore"],
  [cloudcc.FileStore, "fs", "FileStore"],
  [cloudcc.Secret, "secret", "Secret"],
  [cloudcc.Topic, "pubsub", "Topic"],
  [cloudcc.Gateway, "expose", "Gateway"],
];

/** Shims that hand back the library's own client. Nothing to compare
 *  method-by-method; what matters is that they still export connect(). */
const TYPE_PRESERVING = ["redis_ioredis", "redis_node", "orm_pg", "orm_knex"];

/**
 * Public methods of a shim class, read from source.
 *
 * The file is parsed with a regex rather than imported because importing it
 * would pull in the AWS SDK, which this package deliberately does not depend
 * on. The shapes involved are small and entirely under our control.
 */
function shimMethods(module, className) {
  const src = readFileSync(join(SHIM_DIR, `${module}.js`), "utf8");
  const start = src.indexOf(`export class ${className} {`);
  assert.notEqual(start, -1, `class ${className} not found in ${module}.js`);

  const body = src.slice(start);
  const out = new Map();
  for (const m of body.matchAll(/^ {2}(?:async )?([A-Za-z_$][\w$]*)\(([^)]*)\)\s*\{/gm)) {
    const [, name, params] = m;
    if (name === "constructor" || name.startsWith("_")) {
      continue;
    }
    out.set(name, params.trim() === "" ? 0 : params.split(",").length);
  }
  return out;
}

function sdkMethods(cls) {
  const out = new Map();
  for (const name of Object.getOwnPropertyNames(cls.prototype)) {
    if (name === "constructor" || name.startsWith("_")) {
      continue;
    }
    const desc = Object.getOwnPropertyDescriptor(cls.prototype, name);
    if (typeof desc.value === "function") {
      out.set(name, desc.value.length);
    }
  }
  return out;
}

for (const [cls, module, shimClass] of PAIRS) {
  test(`${module}.js's ${shimClass} matches the SDK's`, () => {
    const shim = shimMethods(module, shimClass);
    const local = sdkMethods(cls);

    for (const name of local.keys()) {
      assert.ok(
        shim.has(name),
        `${cls.name} offers ${name}() but _cloudcc_runtime/${module}.js's ${shimClass} does not; ` +
          `a program that works locally would fail once compiled`,
      );
    }
    for (const name of shim.keys()) {
      assert.ok(
        local.has(name),
        `_cloudcc_runtime/${module}.js's ${shimClass} offers ${name}() but the SDK class does not; ` +
          `the editor would not suggest it`,
      );
    }
    for (const [name, arity] of local) {
      // Default parameters do not count toward Function.length, so compare on
      // the required ones only -- an optional argument on one side is not a
      // mismatch a caller can trip over.
      assert.ok(
        shim.get(name) >= arity,
        `${cls.name}.${name}() takes ${arity} required argument(s) but ${shimClass}.${name}() accepts ${shim.get(name)}`,
      );
    }
  });
}

for (const module of [...PAIRS.map((p) => p[1]), ...TYPE_PRESERVING]) {
  test(`${module}.js has an entrypoint`, () => {
    const src = readFileSync(join(SHIM_DIR, `${module}.js`), "utf8");
    const expected = module === "expose" ? "register" : "connect";
    assert.ok(
      new RegExp(`export (async )?function ${expected}\\(`).test(src),
      `_cloudcc_runtime/${module}.js has no ${expected}()`,
    );
  });
}

test("connect is synchronous everywhere it returns a client", () => {
  // Uncompiled, persist() hands back a client immediately. If a shim's
  // connect() were async the same expression would be a client before
  // compiling and a Promise after, and every call on it would break.
  for (const module of [...PAIRS.map((p) => p[1]).filter((m) => m !== "expose"), ...TYPE_PRESERVING]) {
    const src = readFileSync(join(SHIM_DIR, `${module}.js`), "utf8");
    assert.ok(
      !/export async function connect\(/.test(src),
      `_cloudcc_runtime/${module}.js declares connect() async, which would make the compiled binding a Promise`,
    );
  }
});

test("no shim imports the AWS SDK outside the client module", () => {
  for (const [, module] of PAIRS.map((p) => [null, p[1]])) {
    if (module === "expose") {
      continue;
    }
    const src = readFileSync(join(SHIM_DIR, `${module}.js`), "utf8");
    assert.ok(
      !src.includes("@aws-sdk/") || src.includes("./client.js"),
      `${module}.js reaches AWS without going through client.js, so the endpoint override would not apply`,
    );
  }
});
