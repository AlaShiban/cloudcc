// Shared AWS plumbing: endpoint override, region, and dummy credentials.
//
// Every client honours CLOUDCC_AWS_ENDPOINT_URL, so a compiled application can
// be pointed at an emulator with no code change at all.

export function common() {
  const options = {};

  const endpoint = process.env.CLOUDCC_AWS_ENDPOINT_URL;
  if (endpoint) {
    options.endpoint = endpoint;
    // Path-style addressing: an emulator serves every bucket from one host.
    options.forcePathStyle = true;
  }

  const region = process.env.AWS_REGION ?? process.env.AWS_DEFAULT_REGION;
  if (region) {
    options.region = region;
  }

  // Emulators accept any credentials but the SDK refuses to sign without some.
  // Supplying placeholders only when an endpoint override is in play keeps a
  // real deployment on the normal credential chain.
  if (endpoint && !process.env.AWS_ACCESS_KEY_ID) {
    options.credentials = { accessKeyId: "cloudcc-local", secretAccessKey: "cloudcc-local" };
  }
  return options;
}

/**
 * The environment-variable spelling of a capability id.
 *
 * Must agree with sanitize.EnvVar in the compiler and with the identical
 * function in the SDK; a parity test pins all three together.
 */
export function slug(id) {
  return [...id].map((c) => (/[a-zA-Z0-9]/.test(c) ? c.toUpperCase() : "_")).join("");
}

/** Read a required environment binding, or explain what is missing. */
export function env(name, capability, id) {
  const value = process.env[name];
  if (value === undefined) {
    throw new Error(
      `${name} is not set: this process was not wired to the ${capability} ${JSON.stringify(id)}. ` +
        "Environment bindings come from the generated Pulumi stack; when running locally, " +
        "export them from `pulumi stack output --json`.",
    );
  }
  return value;
}
