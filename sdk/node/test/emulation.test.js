/**
 * The local emulations exist so an SDK-annotated app runs on a laptop.
 *
 * These tests pin the behaviour programs actually depend on, and -- just as
 * importantly -- pin the method names, which the compiler's parity test
 * compares against the injected _cloudcc_runtime clients and against the
 * Python SDK.
 */

import assert from "node:assert/strict";
import { mkdtempSync, readFileSync } from "node:fs";
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
  for (const file of ["index.ts", "emulation.ts"]) {
    const src = readFileSync(join(here, "..", "src", file), "utf8");
    assert.ok(!src.includes("@aws-sdk/"), `${file} imports an AWS client`);
    assert.ok(!src.includes("aws-sdk"), `${file} imports an AWS client`);
  }
});

test("persistKv round-trips", () => {
  const pets = cloudcc.persistKv("petsByOwner");
  assert.equal(pets.get("1"), null);

  pets.put("1", { name: "rex" });
  assert.deepEqual(pets.get("1"), { name: "rex" });
  assert.deepEqual(pets.keys(), ["1"]);

  pets.delete("1");
  assert.equal(pets.get("1"), null);
  assert.deepEqual(pets.keys(), []);
});

test("persistKv returns a copy", () => {
  const pets = cloudcc.persistKv("petsByOwner");
  pets.put("1", { name: "rex" });
  const got = pets.get("1");
  got.name = "mutated";
  assert.deepEqual(pets.get("1"), { name: "rex" });
});

test("the same id gives the same store", () => {
  assert.equal(cloudcc.persistKv("shared"), cloudcc.persistKv("shared"));
  assert.notEqual(cloudcc.persistKv("a"), cloudcc.persistKv("b"));
});

test("persistFs round-trips", () => {
  const blobs = cloudcc.persistFs("petAudit");
  assert.deepEqual(blobs.list(), []);
  assert.equal(blobs.exists("a.txt"), false);

  blobs.write("a.txt", Buffer.from("hello"));
  blobs.write("nested/b.txt", Buffer.from("there"));

  assert.equal(blobs.read("a.txt").toString(), "hello");
  assert.equal(blobs.exists("a.txt"), true);
  assert.deepEqual(blobs.list(), ["a.txt", "nested/b.txt"]);
  assert.deepEqual(blobs.list("nested/"), ["nested/b.txt"]);

  blobs.delete("a.txt");
  assert.equal(blobs.exists("a.txt"), false);
  assert.throws(() => blobs.read("a.txt"));
});

test("persistSecret reads the environment", () => {
  const secret = cloudcc.persistSecret("api-key");
  assert.equal(secret.get(), "");

  process.env.CLOUDCC_SECRET_API_KEY = "s3cr3t";
  assert.equal(cloudcc.persistSecret("api-key").get(), "s3cr3t");
  delete process.env.CLOUDCC_SECRET_API_KEY;

  secret.set("overridden");
  assert.equal(secret.get(), "overridden");
});

test("persistRedis operations", () => {
  const cache = cloudcc.persistRedis("sessions");
  assert.equal(cache.get("k"), null);

  cache.set("k", "v");
  assert.equal(cache.get("k"), "v");

  cache.set("k", "v2", 60);
  assert.equal(cache.get("k"), "v2");

  assert.equal(cache.incr("hits"), 1);
  assert.equal(cache.incr("hits", 4), 5);

  cache.delete("k");
  assert.equal(cache.get("k"), null);
});

test("persistOrm gives a local url", () => {
  const db = cloudcc.persistOrm("maindb", { models: ["Row"] });
  const url = db.url();
  assert.ok(url.startsWith("sqlite://"), url);
  assert.ok(url.endsWith("maindb.db"), url);
});

test("pubsub fans out in process", () => {
  const topic = cloudcc.pubsubTopic("petEvents");
  const seen = [];

  topic.subscribe((m) => seen.push(["first", m.id]));
  topic.subscribe((m) => seen.push(["second", m.id]));

  topic.publish({ id: "1" });
  assert.deepEqual(seen, [["first", "1"], ["second", "1"]]);
  assert.equal(topic.subscribers().length, 2);
});

test("subscribe returns the handler, so it reads as a wrapper", () => {
  const topic = cloudcc.pubsubTopic("t");
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

test("resetLocalState clears directories", () => {
  const blobs = cloudcc.persistFs("b");
  blobs.write("a.txt", Buffer.from("x"));
  assert.equal(blobs.exists("a.txt"), true);

  cloudcc.resetLocalState();
  assert.equal(cloudcc.persistFs("b").exists("a.txt"), false);
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
