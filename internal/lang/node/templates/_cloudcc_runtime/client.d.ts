// Type declarations for the injected runtime.
//
// The compiled copy of a TypeScript program is source someone reads, diffs and
// typechecks. Without these, `tsc` on that tree reports every shim import as an
// implicit any, which under `strict` is an error in code the user did not write
// and cannot fix.
//
// A store's client is typed `any` deliberately, not lazily. What `connect`
// hands back is the library's own client -- an ioredis, a pg Pool, a
// DynamoDBClient -- and which one is a fact about the call site the compiler
// already checked. Naming one here would be picking a type this module cannot
// know, and every wrong guess would be an error in correct code.

/** Common client options: the endpoint override and throwaway credentials. */
export function common(): Record<string, unknown>;

/** An id in the spelling environment variables use. */
export function slug(id: string): string;

/** Read a required binding, or throw naming what is missing. */
export function env(name: string, capability: string, id: string): string;
