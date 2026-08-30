export const CALL_KEY: string;
export const ERROR_KEY: string;
export const RESULT_KEY: string;

/** An error raised by the unit that was called, re-thrown on this side. */
export class RemoteError extends Error {
  constructor(type: string, message: string, unit: string, fn: string);
  readonly type: string;
  readonly unit: string;
  readonly fn: string;
}

// A stand-in for another unit's module, so the caller keeps writing
// `await other.whatever(...)`. Which functions exist was checked at compile
// time, which is why this is `any` rather than a list nobody maintains.
export function connect(id: string): any;

export function isCall(event: unknown): boolean;
export function dispatch(module: unknown, event: any): Promise<unknown>;
