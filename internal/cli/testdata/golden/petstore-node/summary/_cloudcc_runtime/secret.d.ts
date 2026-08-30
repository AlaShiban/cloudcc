export class Secret {
  constructor(id: string, arn: string, client: unknown);
  readonly id: string;
  get(): Promise<string>;
  set(value: string): Promise<void>;
}

export function connect(id: string): Secret;
