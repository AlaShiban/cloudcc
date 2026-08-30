// Logging, configured before any user module is evaluated.
//
// This is the one capability with no client to hand back. A program does not
// choose where its logs go -- an operator does, in cloudcc.yaml -- and the call
// sites are identical either way:
//
//     console.log("started");
//
// So what the runtime owes an application is that by the time that line runs,
// stdout is already going where it was configured to go, in a shape the
// destination can parse, with the unit's identity attached.
//
// CloudWatch is the only destination implemented. Choosing another is a compile
// error naming this one, rather than a key that is quietly dropped -- see
// `typeSupport` in internal/provider/aws/support.go.
//
// **Where a vendor plugs in is here**, and nowhere else. A Datadog or Honeycomb
// destination is a different sink installed by configure(); it is not a
// different call in the application and not a wrapper around the logger. That
// narrowness is the whole value of routing this through the compiler.
//
// Configuring on import is deliberate. ES modules evaluate their dependencies
// in order, so an entrypoint that imports this before the application module
// has its logging set up before the application's first statement runs.

const DESTINATION_ENV = "CLOUDCC_LOG_DESTINATION";
const UNIT_ENV = "CLOUDCC_UNIT";

export function destination() {
  return process.env[DESTINATION_ENV] ?? "cloudwatch";
}

export function unit() {
  return process.env[UNIT_ENV] ?? "";
}

/**
 * Point console output at the configured destination.
 *
 * Returns the previous console methods, so a test can put them back.
 */
export function configure() {
  const where = destination();
  if (where !== "cloudwatch") {
    // The compiler rejects this, so reaching it means the bundle and the
    // configuration disagree -- worth failing loudly rather than logging
    // somewhere nobody is looking.
    throw new Error(
      `${DESTINATION_ENV}=${JSON.stringify(where)}, but this runtime only implements ` +
        `cloudwatch. The bundle is older than the configuration that deployed it.`,
    );
  }

  const previous = { log: console.log, info: console.info, warn: console.warn, error: console.error };
  const emit = (level, args) => {
    // CloudWatch parses one JSON object per line into queryable fields, and a
    // line logged from a module shared between units still says which unit
    // logged it -- a fact a shared module cannot know about itself.
    const payload = {
      level,
      message: args.map(render).join(" "),
      unit: unit() || undefined,
      trace: process.env._X_AMZN_TRACE_ID || undefined,
    };
    previous.log(JSON.stringify(payload));
  };

  console.log = (...args) => emit("info", args);
  console.info = (...args) => emit("info", args);
  console.warn = (...args) => emit("warn", args);
  console.error = (...args) => emit("error", args);
  return previous;
}

function render(value) {
  if (typeof value === "string") {
    return value;
  }
  if (value instanceof Error) {
    return value.stack ?? value.message;
  }
  try {
    return JSON.stringify(value);
  } catch {
    return String(value);
  }
}

configure();
