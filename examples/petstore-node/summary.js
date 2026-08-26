// A second execution unit, reached over the wire.
//
// Nothing exposes it to the network. It exists because the API unit calls it,
// and the only way in is `remote`.
//
// Everything exported here is `async`, which the compiler requires of anything
// reached over the wire. That is not a style rule: compiled, each of these is a
// network round trip, and a synchronous signature is the one thing that cannot
// be corrected afterwards -- by then every caller has been written to block on
// it.
import { executionUnit } from "@cloudcompiler/sdk";

executionUnit({ id: "summary" });

/** A one-line description of a pet. Called by the API unit on every write. */
export async function describe(pet) {
  const name = pet?.name ?? "unnamed";
  const species = pet?.species ?? "unknown";
  const age = pet?.age;
  return age === undefined ? `${name} (${species})` : `${name} (${species}, ${age})`;
}

/** Not exported over the wire because it is not exported at all. */
function shout(text) {
  return text.toUpperCase();
}

void shout;
