// The message type is a parameter, defaulting to `any`.
//
// A program that wrote `new Topic<OrderPlaced>()` had a typed channel before
// compiling, and the rewritten copy must not take that away -- but this shim
// cannot know which type, so the default accepts whatever the program's own
// handler declares. The SDK's Topic defaults to Json instead, because there a
// bare `Topic()` should still say "an object went in".
export type Subscriber<T = any> = (message: T) => unknown;

export class Topic<T = any> {
  constructor(id: string, backing: string, address: string, client: unknown);
  readonly id: string;
  publish(message: T): Promise<void>;
  subscribe(fn: Subscriber<T>): Subscriber<T>;
  subscribers(): Subscriber<T>[];
}

export function connect<T = any>(id: string): Topic<T>;

/** Whether an event looks like a delivery from a topic or a queue. */
export function isDelivery(event: unknown): boolean;

export function dispatch(event: any): Promise<unknown[]>;
