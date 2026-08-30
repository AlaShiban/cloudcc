// A client is `any` here for the same reason it is in client.d.ts: `wrap`
// hands back what it was given, and saying so in the type system is the whole
// point -- a program that persisted a DynamoDBClient still holds one.
export const ENV: string;
export const MARKER: string;
export function enabled(): boolean;
export function reset(): void;
export function emit(
  kind: string,
  id: string,
  op: string,
  fields?: { args?: unknown; ret?: unknown; err?: string },
): void;
export function canon(value: unknown): unknown;
export function wrap<T>(client: T, kind: string, id: string, path?: string[]): T;
