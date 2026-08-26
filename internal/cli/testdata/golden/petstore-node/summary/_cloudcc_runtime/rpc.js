// Calls between execution units, backed by Lambda invoke.
//
// Uncompiled, `remote(pricing, { id: "pricing" })` returns the module and
// `await pricing.quote(...)` is an ordinary in-process call. Compiled, this
// module stands in for it: the same await serialises its arguments, invokes
// the other unit's function, and returns what came back.
//
// The wire format is the same JSON envelope the Python runtime uses, and
// deliberately so -- a unit does not need to know what the unit it is calling
// was written in, and a parity test pins the two spellings together.
//
// Two things do not travel: an error arrives as a description rather than as
// the original class, because this bundle does not carry the other unit's code
// and cannot reconstruct one; and anything JSON cannot carry is refused on the
// way out rather than silently reshaped.

import { InvokeCommand, LambdaClient } from "@aws-sdk/client-lambda";

import { common, env, slug } from "./client.js";

/** Envelope keys. The generated entrypoint and this module are the two ends of
 *  one protocol, so the spellings live here. */
export const CALL_KEY = "cloudcc_call";
export const ERROR_KEY = "cloudcc_error";

/**
 * A reply is always an object, never the returned value on its own.
 *
 * A function returning a string would otherwise put a bare scalar on the wire,
 * and a bare scalar is where a reply stops being self-describing: `rex (dog)`
 * is a valid return value and also exactly what a truncated document looks
 * like, and whether it arrives quoted depends on the runtime rather than on
 * the program. Wrapping it costs nine bytes and makes every reply parseable
 * the same way -- which is also what keeps "returned null" and "answered
 * nothing at all" different answers.
 */
export const RESULT_KEY = "cloudcc_result";

/** Return a client for the execution unit declared as remote({ id }). */
export function connect(id) {
  const name = env(`CLOUDCC_UNIT_${slug(id)}_FUNCTION`, "remote", id);
  return makeRemote(id, name);
}

/**
 * A stand-in for another unit's module.
 *
 * A Proxy, so any function the other unit offers can be called without this
 * side holding a list of them: `await pricing.quote(basket)` is unchanged from
 * the uncompiled program. Which functions exist was checked at compile time
 * against the callee's source, which is why there is no table here.
 */
function makeRemote(id, functionName) {
  const state = { id, functionName, client: null };
  return new Proxy(
    {},
    {
      get(_target, property) {
        if (typeof property !== "string" || property.startsWith("_")) {
          return undefined;
        }
        if (property === "then") {
          // Without this an `await` on the handle itself would treat it as a
          // thenable and call `then` on the far side.
          return undefined;
        }
        if (property === "toJSON" || property === Symbol.toStringTag) {
          return undefined;
        }
        return (...args) => invoke(state, property, args);
      },
    },
  );
}

async function invoke(state, functionName, args) {
  state.client ??= new LambdaClient(common());

  let payload;
  try {
    payload = JSON.stringify({
      [CALL_KEY]: { function: functionName, args, kwargs: {} },
    });
  } catch (cause) {
    throw new TypeError(
      `the arguments to ${state.id}.${functionName}() cannot be sent: ${cause.message}. ` +
        "Arguments cross the wire as JSON, which is what lets the two units deploy independently",
    );
  }

  const response = await state.client.send(
    new InvokeCommand({
      FunctionName: state.functionName,
      InvocationType: "RequestResponse",
      Payload: payload,
    }),
  );

  const body = response.Payload ? new TextDecoder().decode(response.Payload) : "";

  // A handler that threw produces FunctionError, which is a different failure
  // from one that returned an error envelope.
  if (response.FunctionError) {
    throw new RemoteError(state.id, functionName, describe(body));
  }
  if (!body) {
    // Not the same as returning null, which arrives as a result envelope
    // carrying null. An empty payload means the other unit answered nothing.
    throw new RemoteError(state.id, functionName, "the reply was empty");
  }

  let result;
  try {
    result = JSON.parse(body);
  } catch {
    throw new RemoteError(
      state.id,
      functionName,
      `the reply was not JSON: ${body.slice(0, 200)}`,
    );
  }

  if (result && typeof result === "object" && ERROR_KEY in result) {
    const detail = result[ERROR_KEY] ?? {};
    throw new RemoteError(
      state.id,
      functionName,
      `${detail.type ?? "Error"}: ${detail.message ?? ""}`,
    );
  }
  if (result && typeof result === "object" && RESULT_KEY in result) {
    return result[RESULT_KEY];
  }
  throw new RemoteError(
    state.id,
    functionName,
    "the reply carried neither a result nor an error, so this unit is not " +
      `answering the protocol its caller was compiled for: ${JSON.stringify(result)}`,
  );
}

/**
 * A call reached the other unit and the other unit failed.
 *
 * Deliberately not the original error class: this bundle does not carry the
 * other unit's code, so there is nothing to reconstruct. What it carries
 * instead is which unit failed and what it said, and it is one class, so a
 * caller can catch every cross-unit failure without importing anything from
 * the other service.
 */
export class RemoteError extends Error {
  constructor(unit, fn, detail) {
    super(`execution unit ${JSON.stringify(unit)} failed in ${fn}(): ${detail}`);
    this.name = "RemoteError";
    this.unit = unit;
    this.function = fn;
    this.detail = detail;
  }
}

function describe(body) {
  try {
    const parsed = JSON.parse(body);
    if (parsed && typeof parsed === "object") {
      return `${parsed.errorType ?? "Error"}: ${parsed.errorMessage ?? ""}`;
    }
    return String(parsed).slice(0, 500);
  } catch {
    return body.slice(0, 500);
  }
}

// ------------------------------------------------------------------ callee

/** Whether an invocation is a call from another execution unit. */
export function isCall(event) {
  return Boolean(event) && typeof event === "object" && CALL_KEY in event;
}

/**
 * Run the requested function on the unit's entry module.
 *
 * Called by the generated Lambda entrypoint. The compiler has already checked
 * that the name exists and is async, so this failing means the deployed bundle
 * and the compile that produced the caller have drifted apart -- which is
 * worth saying plainly rather than throwing a TypeError about undefined.
 */
export async function dispatch(module, event) {
  const request = event[CALL_KEY] ?? {};
  const name = request.function ?? "";
  const args = request.args ?? [];

  if (name.startsWith("_") || typeof module?.[name] !== "function") {
    return {
      [ERROR_KEY]: {
        type: "TypeError",
        message:
          `this unit has no remote function ${JSON.stringify(name)}; ` +
          "the caller was compiled against a different version of it",
      },
    };
  }

  try {
    return { [RESULT_KEY]: await module[name](...args) };
  } catch (error) {
    return {
      [ERROR_KEY]: { type: error?.name ?? "Error", message: error?.message ?? String(error) },
    };
  }
}
