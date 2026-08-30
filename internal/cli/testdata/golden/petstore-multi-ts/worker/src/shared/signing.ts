// The key the worker stamps audit records with.
//
// A secret is one of the two capabilities this SDK still supplies a class for,
// and for the same reason as a topic: there is no client library to wrap. A
// secret is a value the environment holds, not a store with an API.
//
// Imported by the worker alone. The api writes pets; the worker signs the
// record of it having happened, and neither needs what the other holds.

// Injected by cloudcc: runtime clients for this program's declared capabilities.
import * as _cloudccSecret from "../../_cloudcc_runtime/secret.js";

import { createHmac } from "node:crypto";


const auditKey = _cloudccSecret.connect("auditKey");

/**
 * A short signature over an audit record.
 *
 * Truncated, because what it is for here is showing that the secret was fetched
 * and used rather than being a real integrity guarantee.
 */
export async function stamp(petId: string, summary: string): Promise<string> {
  const key = await auditKey.get();
  return createHmac("sha256", key).update(`${petId}:${summary}`).digest("hex").slice(0, 16);
}
