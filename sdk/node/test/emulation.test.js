/**
 * The local emulations exist so an SDK-annotated app runs on a laptop.
 *
 * These tests pin the behaviour programs actually depend on, and -- just as
 * importantly -- pin the method names, which the compiler's parity test
 * compares against the injected _cloudcc_runtime clients and against the
 * Python SDK.
 */

import assert from "node:assert/strict";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { after, beforeEach, test } from "node:test";
import { fileURLToPath } from "node:url";

import * as cloudcc from "../dist/index.js";

const here = fileURLToPath(new URL(".", import.meta.url));

beforeEach(() => {
  process.env.CLOUDCC_LOCAL_STATE_DIR = mkdtempSync(join(tmpdir(), "cloudcc-sdk-"));
  cloudcc.resetLocalState();
});

after(() => cloudcc.resetLocalState());

test("the SDK never imports an AWS client", () => {
  // Cloud access belongs in the injected shims, never in the hint SDK.
  //
  // Comments are stripped before the check rather than searched: the
  // documentation has to be free to name @aws-sdk/client-dynamodb, because
  // wrapping one is now how a key/value store is declared. A test that cannot
  // tell an import from a doc comment would have to be weakened every time the
  // docs improve.
  for (const file of ["index.ts", "emulation.ts"]) {
    const src = readFileSync(join(here, "..", "src", file), "utf8")
      .replace(/\/\*[\s\S]*?\*\//g, "")
      .replace(/^\s*\/\/.*$/gm, "");
    assert.ok(!src.includes("aws-sdk"), `${file} imports an AWS client`);
  }
});

test("persist returns exactly what it was given", () => {
  // The property the whole design rests on. persist is a compile-time hint;
  // uncompiled it must be the identity function, or a program would behave
  // differently depending on whether it had been compiled.
  for (const client of [{}, [1, 2], "text", 42, new cloudcc.Topic()]) {
    assert.equal(cloudcc.persist(client, { id: "anything" }), client);
  }
});

test("persist preserves the type it was handed", () => {
  const topic = new cloudcc.Topic();
  assert.ok(cloudcc.persist(topic, { id: "events" }) instanceof cloudcc.Topic);
});

test("the SDK supplies no data store classes", () => {
  // A store is declared by wrapping the client library you already use. A
  // class of ours would be a dialect nobody else speaks, and its methods would
  // have to be kept in step with the injected runtime's forever -- which is
  // the drift the parity test exists to catch and this rule exists to remove.
  for (const gone of ["KVStore", "FileStore", "DocumentStore", "Queue"]) {
    assert.equal(
      cloudcc[gone],
      undefined,
      `cloudcc.${gone} is a data store class; wrap a real client instead`,
    );
  }
  assert.ok(cloudcc.Topic);
  assert.ok(cloudcc.Secret);
});

test("Secret reads the environment", async () => {
  const secret = cloudcc.persist(new cloudcc.Secret(), { id: "api-key" });
  assert.equal(await secret.get(), "");

  process.env.API_KEY = "s3cr3t";
  assert.equal(await new cloudcc.Secret("API_KEY").get(), "s3cr3t");
  delete process.env.API_KEY;

  await secret.set("overridden");
  assert.equal(await secret.get(), "overridden");
});

test("Topic fans out in process", async () => {
  const topic = cloudcc.persist(new cloudcc.Topic(), { id: "petEvents" });
  const seen = [];

  topic.subscribe((m) => seen.push(["first", m.id]));
  topic.subscribe((m) => seen.push(["second", m.id]));

  await topic.publish({ id: "1" });
  assert.deepEqual(seen, [["first", "1"], ["second", "1"]]);
  assert.equal(topic.subscribers().length, 2);
});

test("subscribe returns the handler, so it reads as a wrapper", () => {
  const topic = cloudcc.persist(new cloudcc.Topic(), { id: "t" });
  const handler = topic.subscribe(() => "handled");
  assert.equal(handler({ id: "x" }), "handled");
});

test("configValue reads its environment variable", () => {
  assert.equal(cloudcc.configValue("log_level", { default: "info" }), "info");
  process.env.CLOUDCC_CONFIG_LOG_LEVEL = "debug";
  assert.equal(cloudcc.configValue("log_level", { default: "info" }), "debug");
  delete process.env.CLOUDCC_CONFIG_LOG_LEVEL;
});

test("expose returns an inert handle", () => {
  const app = {};
  const gateway = cloudcc.expose(app, { id: "pet-api" });
  assert.equal(gateway.id, "pet-api");
  assert.equal(gateway.target, "public");
  assert.equal(gateway.app, app);
  assert.equal(gateway.url(), "");

  process.env.CLOUDCC_GATEWAY_PET_API_URL = "https://example.test";
  assert.equal(gateway.url(), "https://example.test");
  delete process.env.CLOUDCC_GATEWAY_PET_API_URL;
});

test("hint-only functions return quietly", () => {
  assert.equal(cloudcc.executionUnit({ id: "api" }), undefined);
  assert.equal(cloudcc.executionUnit({ id: "api", type: "ecs" }), undefined);
  assert.equal(cloudcc.staticUnit("site", { staticFiles: "./public/**/*" }), undefined);
  assert.equal(cloudcc.embedAssets("./data/*.json"), "./data/*.json");
});

test("resetLocalState clears directories", async () => {
  const root = cloudcc.localRoot();
  mkdirSync(root, { recursive: true });
  writeFileSync(join(root, "leftover"), "x");

  cloudcc.resetLocalState();
  assert.equal(existsSync(root), false);
});

test("slug matches the compiler's spelling", () => {
  // Must agree with sanitize.EnvVar in Go and with the Python SDK.
  const cases = {
    petsByOwner: "PETSBYOWNER",
    "pet-api": "PET_API",
    "log.level": "LOG_LEVEL",
    "9lives": "9LIVES",
  };
  assert.equal(typeof cloudcc.slug, "function", "slug must be exported for this to mean anything");
  for (const [input, want] of Object.entries(cases)) {
    assert.equal(cloudcc.slug(input), want);
  }
});
