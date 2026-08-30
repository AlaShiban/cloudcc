// Runtime configuration values, delivered as environment variables.

import { slug } from "./client.js";

/**
 * Return the configured value for `id`.
 *
 * `secret` is a compile-time signal -- it decides whether the generated stack
 * stores the value as a Pulumi secret -- and has no effect on the read.
 */
export function value(id, options = {}) {
  return process.env[`CLOUDCC_CONFIG_${slug(id)}`] ?? options.default ?? "";
}
