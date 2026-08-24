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

test("persist returns exactly what it was given", () => {
  // The property the whole design rests on. persist is a compile-time hint;
  // uncompiled it must be the identity function, or a program would behave
  // differently depending on whether it had been compiled.
  for (const client of [{}, [1, 2], "text", 42, new cloudcc.KVStore()]) {
    assert.equal(cloudcc.persist(client, { id: "anything" }), client);
  }
});

test("persist preserves the type it was handed", () => {
  const store = new cloudcc.KVStore();
  assert.ok(cloudcc.persist(store, { id: "kv" }) instanceof cloudcc.KVStore);
});

test("KVStore round-trips", async () => {
  const pets = cloudcc.persist(new cloudcc.KVStore(), { id: "petsByOwner" });
  assert.equal(await pets.get("1"), null);

  await pets.put("1", { name: "rex" });
  assert.deepEqual(await pets.get("1"), { name: "rex" });
  assert.deepEqual(await pets.keys(), ["1"]);

  await pets.delete("1");
  assert.equal(await pets.get("1"), null);
  assert.deepEqual(await pets.keys(), []);
});

test("KVStore returns a copy", async () => {
  const pets = cloudcc.persist(new cloudcc.KVStore(), { id: "petsByOwner" });
  await pets.put("1", { name: "rex" });
  const got = await pets.get("1");
  got.name = "mutated";
  assert.deepEqual(await pets.get("1"), { name: "rex" });
});

test("a KVStore really persists", async () => {
  // A verb called persist handing back a Map that forgets on exit would be a
  // poor joke, so the local store is file-backed.
  const path = join(process.env.CLOUDCC_LOCAL_STATE_DIR, "explicit.json");
  await new cloudcc.KVStore(path).put("1", { name: "rex" });
  assert.deepEqual(await new cloudcc.KVStore(path).get("1"), { name: "rex" });
});

test("two KVStores are independent", async () => {
  const a = new cloudcc.KVStore();
  const b = new cloudcc.KVStore();
  await a.put("k", { v: 1 });
  assert.equal(await b.get("k"), null);
});

test("FileStore round-trips", async () => {
  // Node has no pathlib, so unlike the Python SDK this one is a class we
  // supply -- and the injected shim has to match it method for method.
  const blobs = cloudcc.persist(new cloudcc.FileStore(), { id: "petAudit" });
  assert.deepEqual(await blobs.list(), []);
  assert.equal(await blobs.exists("a.txt"), false);

  await blobs.write("a.txt", Buffer.from("hello"));
  await blobs.write("nested/b.txt", Buffer.from("there"));

  assert.equal((await blobs.read("a.txt")).toString(), "hello");
  assert.equal(await blobs.exists("a.txt"), true);
  assert.deepEqual(await blobs.list(), ["a.txt", "nested/b.txt"]);
  assert.deepEqual(await blobs.list("nested/"), ["nested/b.txt"]);

  await blobs.delete("a.txt");
  assert.equal(await blobs.exists("a.txt"), false);
  await assert.rejects(() => blobs.read("a.txt"));
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
  const blobs = new cloudcc.FileStore();
  await blobs.write("a.txt", Buffer.from("x"));
  assert.equal(await blobs.exists("a.txt"), true);

  cloudcc.resetLocalState();
  assert.equal(await blobs.exists("a.txt"), false);
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
