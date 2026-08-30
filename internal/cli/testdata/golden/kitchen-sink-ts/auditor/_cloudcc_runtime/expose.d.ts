export class Gateway {
  constructor(id: string, target?: string, app?: unknown);
  readonly id: string;
  readonly target: string;
  readonly app: unknown;
  url(): string;
}

export function register(app: unknown, options?: { id?: string; target?: string }): Gateway;
