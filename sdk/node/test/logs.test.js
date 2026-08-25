/**
 * The logging shim is the one runtime module with no SDK counterpart.
 *
 * There is nothing to compare it against, because there is nothing for a
 * program to declare: where logs go is chosen in cloudcc.yaml, and the call
 * sites -- `console.log(...)` -- are identical either way. What is worth
 * testing is the contract the runtime owes an application: that by the time
 * user code runs, output is going where it was configured to go, and that a
 * destination this runtime cannot serve fails loudly rather than logging
 * somewhere nobody is looking.
 *
 * Unlike the other shims it imports nothing at all, so it can be executed here
 * rather than only read -- this package still does not depend on the AWS SDK.
 */

import assert from "node:assert/strict";
import { existsSync } from "node:fs";
import { join } from "node:path";
import { test } from "node:test";
import { fileURLToPath, pathToFileURL } from "node:url";

const here = fileURLToPath(new URL(".", import.meta.url));
const SHIM = join(here, "..", "..", "..", "internal", "lang", "node", "templates", "_cloudcc_runtime", "logs.js");

let counter = 0;

/**
 * Import a fresh copy of the shim with the environment set.
 *
 * A query string defeats the module cache, so each call evaluates the module
 * again -- which matters because importing it configures on the way in, and
 * that is the behaviour the entrypoints depend on.
 */
async function load(destination, unit) {
  process.env.CLOUDCC_LOG_DESTINATION = destination;
  process.env.CLOUDCC_UNIT = unit;
  counter += 1;
  return import(`${pathToFileURL(SHIM).href}?copy=${counter}`);
}

/**
 * Run fn with the console captured, and put the console back afterwards.
 *
 * configure() replaces console.* and holds the previous functions as its sink,
 * so capturing before calling it is what makes the emitted lines visible.
 */
function capture(fn) {
  const lines = [];
  const original = { log: console.log, info: console.info, warn: console.warn, error: console.error };
  console.log = (...args) => lines.push(args.join(" "));
  try {
    fn();
  } finally {
    Object.assign(console, original);
  }
  return lines;
}

test("the compiler's shim templates are reachable", () => {
  assert.ok(
    existsSync(SHIM),
    `${SHIM} not found. This suite checks this package against the compiler's ` +
      `injected shims, so it has to run from a cloudcc checkout.`,
  );
});

test("lines are JSON with the unit attached", async () => {
  const logs = await load("cloudwatch", "api");
  const lines = capture(() => {
    logs.configure();
    console.log("started");
  });

  assert.equal(lines.length, 1);
  assert.deepEqual(JSON.parse(lines[0]), {
    level: "info",
    message: "started",
    // A module shared between units cannot know which unit is running it, so
    // the environment says and every line carries it.
    unit: "api",
  });
});

test("every level goes to the same sink, tagged", async () => {
  const logs = await load("cloudwatch", "worker");
  const lines = capture(() => {
    logs.configure();
    console.warn("careful");
    console.error("boom");
  });

  assert.deepEqual(
    lines.map((line) => JSON.parse(line).level),
    ["warn", "error"],
  );
});

test("a destination this runtime cannot serve fails loudly", async () => {
  // The compiler refuses these, so reaching one means the bundle and the
  // configuration that deployed it disagree -- which is worth a crash rather
  // than logs quietly going nowhere.
  await assert.rejects(
    () => load("datadog", "api"),
    (err) => {
      assert.match(err.message, /datadog/);
      assert.match(err.message, /cloudwatch/);
      return true;
    },
  );
});
