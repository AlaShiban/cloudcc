// The second unit: a TypeScript function, called by the container.
//
// It exists so this example covers both compute types in one deploy. The web
// unit is a container; this one is a function, and both are TypeScript — so a
// bundle that failed to load would show up as the two halves of the
// differential run disagreeing, rather than as something nobody exercises.
//
// Uncompiled, `remote` hands the caller this module and the await below is an
// ordinary in-process call, so the whole application still runs as one process.
// Compiled, this module is not in the caller's bundle at all and the same await
// is a Lambda invocation.

import { executionUnit } from "@cloudcompiler/sdk";

import type { Pet } from "@/store";

executionUnit({ id: "summary" });

/**
 * A sentence about a pet.
 *
 * `async` is not decoration: a remote call is a network round trip once
 * compiled, and the compiler rejects a non-async function reached through
 * `remote` rather than letting the await silently mean something different on
 * either side.
 */
export async function describe(pet: Pet): Promise<string> {
  const age = pet.age === 1 ? "1 year" : `${pet.age} years`;
  return `${pet.name} is a ${pet.species}, ${age} old`;
}
