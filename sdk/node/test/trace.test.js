/**
 * The tracer is the instrument the correctness comparison reads, so it earns
 * more scepticism than the things it measures.
 *
 * Two properties matter and they pull against each other:
 *
 *   * It records what happened -- operation, logical id, arguments, result.
 *   * It changes nothing. The runtime hands back the library's own client on
 *     purpose (see kv.js); a Proxy that broke a method, a promise or a
 *     property would break the program. Worse, it would break *both* halves
 *     identically, and the trace diff would still pass -- an instrument that
 *     lies quietly is worse than no instrument at all.
 *
 * The transparency tests are therefore not padding. They are the half of this
 * file that stops the other half from being believed too easily.
 */

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

import { MARKER, canon, emit, enabled, reset, wrap } from "../src/trace.js";

const here = dirnameOf(import.meta.url);
const SHIM = join(here, "..", "..", "..", "internal", "lang", "node",
  "templates", "_cloudcc_runtime", "trace.js");

function dirnameOf(url) {
  const path = fileURLToPath(url);
  return path.slice(0, path.lastIndexOf("/"));
}

/** Run `fn` with tracing on, returning the events it emitted.
 *
 * Always async, even for a synchronous `fn`. The first cut restored stderr in
 * a `finally`, which runs when the try block *returns* -- so for an async fn
 * the capture was torn down before the awaited work ran and three tests saw no
 * events at all. Awaiting inside the try is the fix, and the reason this
 * helper is not shaped the obvious way.
 */
async function record(fn) {
  const written = [];
  const original = process.stderr.write.bind(process.stderr);
  process.env.CLOUDCC_TRACE = "1";
  process.stderr.write = (chunk) => {
    written.push(String(chunk));
    return true;
  };
  try {
    const value = await fn();
    return { value, events: parse(written) };
  } finally {
    process.stderr.write = original;
    delete process.env.CLOUDCC_TRACE;
    reset();
  }
}

function parse(chunks) {
  return chunks
    .join("")
    .split("\n")
    .filter((line) => line.startsWith(MARKER))
    .map((line) => JSON.parse(line.slice(MARKER.length).trim()));
}

test("the SDK copy is byte-identical to the injected one", async () => {
  // Both halves must observe through the same code. If the two drifted, a
  // difference in the trace would no longer distinguish "the program did
  // something different" from "the instrument did something different", and
  // the comparison would be measuring itself.
  const injected = readFileSync(SHIM);
  const vendored = readFileSync(join(here, "..", "src", "trace.js"));
  assert.ok(
    injected.equals(vendored),
    "sdk/node/src/trace.js has drifted from the injected _cloudcc_runtime/trace.js; copy one over the other",
  );
});

test("off by default, and the very same object comes back", async () => {
  delete process.env.CLOUDCC_TRACE;
  assert.equal(enabled(), false);
  const client = { get() {} };
  assert.equal(wrap(client, "kv", "pets"), client);
});

test("an operation is recorded with its arguments and result", async () => {
  const { events } = await record(() => {
    const store = wrap({ put: (k, v) => ({ ok: true, k, v }) }, "kv", "pets");
    return store.put("1", { name: "rex" });
  });
  assert.equal(events.length, 1);
  assert.equal(events[0].kind, "kv");
  assert.equal(events[0].id, "pets");
  assert.equal(events[0].op, "put");
  assert.deepEqual(events[0].args, ["1", { name: "rex" }]);
  assert.deepEqual(events[0].ret, { k: "1", ok: true, v: { name: "rex" } });
});

test("an AWS command is recorded by its own name, not as `send`", async () => {
  // `client.send(new PutItemCommand({...}))` recorded as "send" with an opaque
  // object would say nothing about what the program asked the store to do.
  class PutItemCommand {
    constructor(input) {
      this.input = input;
    }
  }
  const { events } = await record(async () => {
    const client = wrap({ async send() { return { Attributes: {} }; } }, "kv", "pets");
    return client.send(new PutItemCommand({ TableName: "pets", Item: { id: "1" } }));
  });
  assert.equal(events[0].op, "send.PutItemCommand");
  assert.deepEqual(events[0].args, [{ Item: { id: "1" }, TableName: "pets" }]);
});

test("a rejected promise is recorded and still rejects", async () => {
  const { events } = await record(async () => {
    const client = wrap({ async send() { throw new TypeError("no"); } }, "kv", "pets");
    await assert.rejects(() => client.send({}));
    return null;
  });
  assert.equal(events[0].err, "TypeError");
  assert.equal("ret" in events[0], false);
});

test("the resolved value is recorded, not the promise", async () => {
  const { events, value } = await record(async () => {
    const client = wrap({ async get() { return { name: "rex" }; } }, "kv", "pets");
    return client.get("1");
  });
  assert.deepEqual(value, { name: "rex" });
  assert.deepEqual(events[0].ret, { name: "rex" });
});

test("a callable client is a seam -- knex is called, not just read", async () => {
  const { events } = await record(() => {
    const db = wrap(Object.assign((table) => ({ table }), { raw: () => {} }), "orm", "shopdb");
    return db("pets");
  });
  assert.equal(events.length, 1);
  assert.equal(events[0].op, "call");
  assert.deepEqual(events[0].args, ["pets"]);
});

test("a thenable query builder is still chainable, and its rows are recorded", async () => {
  // knex returns a builder that is *thenable* -- `db("pets")` can be awaited --
  // and is also still chainable: `.where(...)` comes next. The first cut of
  // this tracer saw `.then` and treated the builder as a promise, which
  // replaced it with a plain Promise; `.where` stopped existing and every
  // route touching the database answered 500.
  //
  // It did so in *both halves identically*, so the response comparison passed
  // and called it agreement. Nothing in this file caught it either, because
  // every stub above is a plain object -- which is why this test exists and why
  // it models the awkward shape rather than a convenient one.
  const builder = (rows) => ({
    where() {
      return builder(rows.filter((r) => r.id === "1"));
    },
    then(resolve, reject) {
      return Promise.resolve(rows).then(resolve, reject);
    },
  });

  const { events, value } = await record(async () => {
    const db = wrap(() => builder([{ id: "1" }, { id: "2" }]), "orm", "shopdb");
    return db().where({ id: "1" });
  });

  assert.deepEqual(value, [{ id: "1" }]);
  const settled = events.find((e) => e.ret !== undefined);
  assert.deepEqual(settled.ret, [{ id: "1" }]);
  assert.equal(settled.op, "call.where");
});

test("a library object is named, not opened", async () => {
  // `knex.raw(...)` returns a Raw. Opening it reached the knex client, the pg
  // driver and its type tables: 173KB per event, differing between halves
  // because connection settings legitimately differ -- and carrying the
  // database user and password into a trace that a Lambda ships to CloudWatch.
  //
  // The SQL is already recorded as the argument to `raw`, so naming the object
  // loses nothing.
  class Raw {
    constructor() {
      this.client = { config: { connection: { password: "hunter2" } } };
      this.sql = "breeds.seen + 1";
    }
  }
  const rendered = JSON.stringify(canon({ seen: new Raw() }));
  assert.equal(rendered, JSON.stringify({ seen: "<Raw>" }));
  assert.equal(rendered.includes("hunter2"), false);
});

test("a result object is opened for its rows", async () => {
  // The exception that earns its keep: pg answers a query with a Result whose
  // rows are the entire point of the call.
  class Result {
    constructor(rows) {
      this.rows = rows;
      this.command = "SELECT";
    }
  }
  assert.deepEqual(canon(new Result([{ id: 1 }])), [{ id: 1 }]);
});

test("transport metadata is dropped", async () => {
  // The AWS SDK returns $metadata -- HTTP status, a fresh request id, attempt
  // counts -- beside every answer. Left in, two identical runs would differ on
  // every single event and the comparison would be worthless.
  assert.deepEqual(
    canon({ Item: { id: "1" }, $metadata: { httpStatusCode: 200, requestId: "abc" } }),
    { Item: { id: "1" } },
  );
});

test("timestamps are flattened and uuids numbered in first-seen order", async () => {
  reset();
  const a = "3f2504e0-4f89-11d3-9a0c-0305e82c3301";
  const b = "550e8400-e29b-41d4-a716-446655440000";
  assert.equal(canon(new Date()), "<time>");
  assert.deepEqual(canon([a, b, a]), ["<uuid:1>", "<uuid:2>", "<uuid:1>"]);
  reset();
});

test("object key order is never a difference", async () => {
  const { events } = await record(() => {
    const store = wrap({ put: () => null }, "kv", "pets");
    store.put({ b: 1, a: 2 });
    store.put({ a: 2, b: 1 });
    return null;
  });
  assert.deepEqual(events[0], events[1]);
});

// ------------------------------------------------------------ transparency

test("the return value reaches the caller unchanged", async () => {
  const { value } = await record(() => {
    const store = wrap({ get: () => ({ name: "rex" }) }, "kv", "pets");
    return store.get();
  });
  assert.deepEqual(value, { name: "rex" });
});

test("a thrown error still reaches the caller", async () => {
  await record(() => {
    const store = wrap({ boom() { throw new RangeError("x"); } }, "kv", "pets");
    assert.throws(() => store.boom(), RangeError);
    return null;
  });
});

test("properties, writes and `in` pass through to the real object", async () => {
  const target = { items: { a: 1 } };
  await record(() => {
    const store = wrap(target, "kv", "pets");
    assert.deepEqual(store.items, { a: 1 });
    assert.ok("items" in store);
    store.items = { b: 2 };
    return null;
  });
  assert.deepEqual(target.items, { b: 2 });
});

test("`this` is still the real object inside a method", async () => {
  // Binding the method to the proxy instead of the target would make every
  // internal property access go through the recorder, which both floods the
  // trace and changes what a library sees of itself.
  const { value } = await record(() => {
    const store = wrap({ n: 41, bump() { return ++this.n; } }, "kv", "pets");
    return store.bump();
  });
  assert.equal(value, 42);
});

test("emit writes nothing when tracing is off", async () => {
  delete process.env.CLOUDCC_TRACE;
  const written = [];
  const original = process.stderr.write.bind(process.stderr);
  process.stderr.write = (chunk) => {
    written.push(String(chunk));
    return true;
  };
  try {
    emit("kv", "pets", "put", { args: [1] });
  } finally {
    process.stderr.write = original;
  }
  assert.equal(written.join("").includes(MARKER), false);
});
