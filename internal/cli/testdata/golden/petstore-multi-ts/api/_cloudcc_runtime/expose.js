// Handle returned by a rewritten `expose` call.

import { slug } from "./client.js";

/**
 * Record the exposed application and return its gateway handle.
 *
 * The application object itself is what the generated Lambda entrypoint
 * imports; this call exists so the deployed URL is reachable from inside the
 * program.
 */
export function register(app, options = {}) {
  return new Gateway(options.id ?? "main", options.target ?? "public", app);
}

export class Gateway {
  constructor(id, target = "public", app = null) {
    this.id = id;
    this.target = target;
    this.app = app;
  }

  url() {
    return process.env[`CLOUDCC_GATEWAY_${slug(this.id)}_URL`] ?? "";
  }
}
