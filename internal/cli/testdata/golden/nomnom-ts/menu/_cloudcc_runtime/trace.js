// Record what a program does at its cloud seams, so two runs can be compared.
//
// The suite's older correctness check compares HTTP responses before and after
// compiling. That is necessary and not sufficient: it sees only what a route
// chose to return. A compiled program that writes to the wrong table, drops a
// publish, or never reads the secret it was given can answer every request
// byte-identically and pass.
//
// A trace closes that gap by recording the seams themselves: every call the
// program makes through a persisted client, in order, with its arguments and
// its result. Two runs with the same trace did the same work.
//
// The design constraint that shapes this: the runtime hands back the library's
// own object -- a DynamoDBClient, a knex instance, a node-redis client -- and
// that is deliberate (see kv.js). A permanent wrapper would undo it. So
// tracing is opt-in: with CLOUDCC_TRACE unset, `wrap` returns the client
// unchanged and this module costs one environment lookup. Only when it is set
// does a Proxy appear, and the Proxy forwards everything.
//
// Both halves record through this same file. The compiled copy gets it
// injected; the SDK vendors it verbatim, pinned byte-identical by a test. If
// the observer differed between halves, a difference in the trace would not
// tell you whether the program or the instrument had changed.
//
// Where the events go
// -------------------
// Stderr, tagged with a marker, because that is the only channel both halves
// share: a local process has a filesystem, a Lambda does not, and its stderr is
// already shipped to CloudWatch.
//
// What is normalised, and what that costs
// ---------------------------------------
// Each of these removes a class of difference this check could catch, so each
// is a deliberate trade:
//
//   * Physical resource names are never recorded; the logical id from
//     `persist({id})` is, and it is identical in both halves by construction.
//   * Timestamps become "<time>"; they always differ.
//   * UUID-shaped values become "<uuid:N>" in first-seen order, so two runs
//     that mint ids in the same order agree while one that mints a different
//     *number* of them still diverges.
//   * `$metadata` is dropped -- the AWS SDK returns HTTP status, request ids
//     and attempt counts beside every answer, none of which is the program's
//     behaviour and all of which differs per call.
//
// Nothing else is normalised. Values, orderings and error types are compared
// as they are, because that is the point.

/** Set to "1"/"stderr" to trace. */
export const ENV = "CLOUDCC_TRACE";

/** Prefix on every emitted line. Must match trace.py. */
export const MARKER = "##cloudcc-trace##";

/** Keys describing the transport, or the library's own bookkeeping, rather
 * than the program. `__knexQueryUid` is a fresh random id knex stamps on every
 * query it builds: real, and no more the program's behaviour than a request id
 * is. */
const DROP_KEYS = new Set([
  "$metadata", "ResponseMetadata", "ConsumedCapacity", "__knexQueryUid",
]);

const UUID_RE =
  /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/;

const ids = new Map();

/** Whether tracing is on. Off is the default. */
export function enabled() {
  return Boolean(process.env[ENV]);
}

/** Forget generated-id numbering. For tests running several cases. */
export function reset() {
  ids.clear();
}

/** Record one seam event. */
export function emit(kind, id, op, fields = {}) {
  if (!enabled()) return;
  const rec = { kind, id, op };
  if ("args" in fields) rec.args = canon(fields.args);
  if ("ret" in fields) rec.ret = canon(fields.ret);
  if ("err" in fields) rec.err = fields.err;
  process.stderr.write(MARKER + " " + stableStringify(rec) + "\n");
}

/** JSON with object keys in a stable order, so key order is never a diff. */
function stableStringify(value) {
  return JSON.stringify(value, (_key, v) => {
    if (v && typeof v === "object" && !Array.isArray(v)) {
      return Object.fromEntries(Object.keys(v).sort().map((k) => [k, v[k]]));
    }
    return v;
  });
}

/** Reduce a value to something two runs can be compared on. */
export function canon(value) {
  return canonAt(value, 0);
}

function canonAt(v, depth) {
  if (depth > 12) return "<deep>";
  if (v === null || v === undefined) return null;

  const t = typeof v;
  if (t === "boolean" || t === "number") return v;
  if (t === "bigint") return Number(v);
  if (t === "string") return canonString(v);
  if (t === "function") return "<function>";
  if (t === "symbol") return "<symbol>";

  if (v instanceof Date) return "<time>";
  if (v instanceof Error) return "<" + v.name + ">";

  if (v instanceof Uint8Array || (typeof Buffer !== "undefined" && Buffer.isBuffer(v))) {
    const b = Buffer.from(v);
    return b.length <= 64 ? "<b64:" + b.toString("base64") + ">" : "<bytes:" + b.length + ">";
  }

  if (Array.isArray(v)) return v.map((x) => canonAt(x, depth + 1));
  if (v instanceof Set) {
    return [...v].map((x) => canonAt(x, depth + 1)).sort((a, b) =>
      String(a) < String(b) ? -1 : String(a) > String(b) ? 1 : 0);
  }
  if (v instanceof Map) {
    return canonAt(Object.fromEntries(v), depth + 1);
  }

  if (isProxy(v)) return canonAt(target(v), depth + 1);

  if (t === "object") {
    // A plain object is data -- a row, a command's input, a response body --
    // and is opened. A class instance is a library object and is named, not
    // opened.
    //
    // That distinction is not a nicety. `knex.raw(...)` returns a `Raw`, and
    // opening it reached the knex client, the pg driver and its type tables:
    // 173KB per event, differing between halves because the connection
    // settings legitimately differ -- and carrying the database user and
    // password into a trace that a Lambda ships to CloudWatch (D21). The SQL
    // itself is already recorded as the argument to `raw`, so naming the
    // object loses nothing worth having.
    const name = v.constructor && v.constructor.name;
    if (name && name !== "Object" && !name.endsWith("Command")) {
      // Except the shapes that *are* the data: pg answers a query with a
      // Result whose rows are the point of the call.
      if (Array.isArray(v.rows)) return canonAt(v.rows, depth + 1);
      return "<" + name + ">";
    }
    const out = {};
    for (const k of Object.keys(v).sort()) {
      if (DROP_KEYS.has(k)) continue;
      out[k] = canonAt(v[k], depth + 1);
    }
    return out;
  }
  return "<" + t + ">";
}

function canonString(s) {
  if (UUID_RE.test(s)) {
    if (!ids.has(s)) ids.set(s, "<uuid:" + (ids.size + 1) + ">");
    return ids.get(s);
  }
  return s;
}

// --------------------------------------------------------------------------
// The proxy
//
// Transparent by construction: every property is read from the target and
// every call forwarded. It records on the way through.
// --------------------------------------------------------------------------

const TARGET = Symbol("cloudccTraceTarget");

function isProxy(v) {
  return Boolean(v && typeof v === "object" && v[TARGET]);
}

function target(v) {
  return v[TARGET];
}

/** Return `client` unchanged, or a recording proxy when tracing is on. */
export function wrap(client, kind, id, path = []) {
  if (!enabled()) return client;
  if (client === null || client === undefined) return client;
  const t = typeof client;
  if (t !== "object" && t !== "function") return client;
  if (isProxy(client)) return client;

  return new Proxy(client, {
    get(obj, prop, receiver) {
      if (prop === TARGET) return obj;
      const value = Reflect.get(obj, prop, receiver);
      if (typeof prop === "symbol") return value;
      // `then` is how a builder is awaited, and it must keep the promise
      // protocol exactly: the awaiting machinery cares that `resolve` is
      // called, and wrapping it like an ordinary method would both mangle the
      // protocol and record the two callbacks as arguments. Recording what it
      // settles to is the useful thing, and it arrives with the whole chain
      // that produced it as the operation.
      if (prop === "then" && typeof value === "function" && isThenable(obj)) {
        const op = path.join(".");
        return (onResolved, onRejected) =>
          value.call(
            obj,
            (settled) => {
              emit(kind, id, op, { ret: settled });
              return onResolved ? onResolved(settled) : settled;
            },
            (err) => {
              emit(kind, id, op, { err: errName(err) });
              if (onRejected) return onRejected(err);
              throw err;
            },
          );
      }
      // `catch` and `finally` are part of that same protocol; forwarding them
      // untouched keeps a builder a builder.
      if ((prop === "catch" || prop === "finally") && typeof value === "function") {
        return value.bind(obj);
      }
      if (typeof value === "function") {
        return recorded(value.bind(obj), kind, id, [...path, String(prop)]);
      }
      return value;
    },
    // knex is called as a function -- `db("pets").where(...)` -- so the client
    // itself is callable and the call is a seam.
    apply(obj, thisArg, args) {
      return recorded(obj.bind(thisArg), kind, id, path.length ? path : ["call"])(...args);
    },
    set(obj, prop, value) {
      return Reflect.set(obj, prop, value);
    },
    has(obj, prop) {
      return Reflect.has(obj, prop);
    },
  });
}

/** Wrap a callable so invoking it is recorded. */
function recorded(fn, kind, id, path) {
  return function (...args) {
    let op = path.join(".");
    let recordedArgs = args;

    // The AWS SDK v3 shape is `client.send(new PutItemCommand({...}))`. The
    // command's name is the operation; recording it as "send" with an opaque
    // object would tell you nothing about what the program asked for.
    if (path[path.length - 1] === "send" && args.length === 1 && args[0]) {
      const name = args[0].constructor && args[0].constructor.name;
      if (name && name.endsWith("Command")) {
        op = path.slice(0, -1).concat("send", name).join(".");
        recordedArgs = [args[0].input];
      }
    }

    let result;
    try {
      result = fn(...args);
    } catch (err) {
      emit(kind, id, op, { args: recordedArgs, err: errName(err) });
      throw err;
    }

    // A query builder is *thenable* but is not a promise: knex returns one
    // from `db("pets")` and you go on chaining `.where(...)` on it. Treating
    // it as a promise here -- which the first cut did -- replaces the builder
    // with a plain Promise, `.where` stops existing, and every route that
    // touches the database answers 500. In both halves identically, which is
    // exactly the shape of breakage a response diff cannot see.
    //
    // So only a real Promise settles here. A thenable is followed instead, and
    // its result is recorded when something finally awaits it -- see the
    // `then` case in the proxy below.
    if (result instanceof Promise) {
      return result.then(
        (value) => {
          emit(kind, id, op, { args: recordedArgs, ret: value });
          return value;
        },
        (err) => {
          emit(kind, id, op, { args: recordedArgs, err: errName(err) });
          throw err;
        },
      );
    }

    if (isThenable(result)) {
      // The interesting value is what it resolves to, not the builder.
      emit(kind, id, op, { args: recordedArgs });
      return follow(result, kind, id, path);
    }
    emit(kind, id, op, { args: recordedArgs, ret: result });
    return follow(result, kind, id, path);
  };
}

/** Keep tracing through a returned object; leave plain data alone. */
function follow(v, kind, id, path) {
  if (v === null || v === undefined) return v;
  const t = typeof v;
  if (t !== "object" && t !== "function") return v;
  if (v instanceof Date || v instanceof Uint8Array || Array.isArray(v)) return v;
  // A plain object is data -- a row, a response -- and wrapping it would put a
  // proxy around every value the program reads. A *thenable* plain object is
  // not data: it is a query builder that happens not to be a class instance,
  // and leaving it unwrapped loses everything it goes on to do.
  if (v.constructor === Object && !isThenable(v)) return v;
  return wrap(v, kind, id, path);
}

/** Thenable, but not necessarily a promise: a knex builder is one. */
function isThenable(v) {
  return Boolean(v && (typeof v === "object" || typeof v === "function") &&
    typeof v.then === "function");
}

function errName(err) {
  if (!err) return "Error";
  return err.name || (err.constructor && err.constructor.name) || "Error";
}
