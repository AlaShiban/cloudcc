export function target(id: string): { url: string; secretArn: string };

/**
 * What a driver should be given for the password, and nothing when there is
 * none to give.
 *
 * Three shapes, because the distinction matters: an async provider when a
 * managed secret holds the password, a literal when the URL carries it, and an
 * empty object when neither -- which lets the driver fall back to its own
 * resolution rather than being handed an empty string, which Postgres rejects
 * outright.
 */
export function credentials(id: string): { password?: string | (() => Promise<string>) };

export function parts(id: string): {
  host: string;
  port: number;
  user: string;
  database: string;
  mysql: boolean;
};
