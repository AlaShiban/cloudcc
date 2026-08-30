// The worker's own relational store, reached through `pg` directly.
//
// A second database rather than a second view of the first, and deliberately
// so. The worker has no business reading the pet catalogue, and because
// permissions and environment are both derived from what a unit bundles, not
// importing `catalogue.ts` is how it ends up without access to `petsdb` or to
// the cache.
//
// It is also the other half of a pair: `catalogue.ts` declares a Knex instance
// and this file declares a `pg` Pool. Two libraries for one capability, in one
// application, and the compiler records which one each unit asked for so the
// shim hands back the same kind.
//
// Uncompiled this talks to a local Postgres:
//
//     docker run -d -p 5432:5432 -e POSTGRES_USER=ccadmin \
//       -e POSTGRES_DB=auditdb -e POSTGRES_HOST_AUTH_METHOD=trust postgres:16-alpine

// Injected by cloudcc: runtime clients for this program's declared capabilities.
import * as _cloudccOrm from "../../_cloudcc_runtime/orm_pg.js";

import { Pool } from "pg";

/** One audited event: which pet, and the signature that was written. */
export interface Audit {
  pet_id: string;
  summary: string;
  signature: string;
  revision: number;
}

const db = _cloudccOrm.connect("auditdb") as Pool;

let schemaReady: Promise<unknown> | null = null;

// Memoised, and not merely as an optimisation: `IF NOT EXISTS` is idempotent
// but not concurrency-safe, and two connections running it at the same moment
// race between the catalogue check and the create.
function ensureSchema(): Promise<unknown> {
  schemaReady ??= db.query(`
    CREATE TABLE IF NOT EXISTS audits (
      pet_id TEXT PRIMARY KEY,
      summary TEXT NOT NULL DEFAULT '',
      signature TEXT NOT NULL DEFAULT '',
      revision INTEGER NOT NULL DEFAULT 0
    )
  `);
  return schemaReady;
}

/** Write one audit row, and return how many times this pet has been seen. */
export async function record(
  petId: string,
  summary: string,
  signature: string,
): Promise<number> {
  await ensureSchema();
  const out = await db.query<{ revision: number }>(
    `INSERT INTO audits (pet_id, summary, signature, revision) VALUES ($1, $2, $3, 1)
       ON CONFLICT (pet_id) DO UPDATE
       SET summary = $2, signature = $3, revision = audits.revision + 1
       RETURNING revision`,
    [petId, summary, signature],
  );
  return Number(out.rows[0].revision);
}

/** How many distinct pets the ledger holds. */
export async function audited(): Promise<number> {
  await ensureSchema();
  const out = await db.query<{ count: string }>("SELECT count(*) AS count FROM audits");
  return Number(out.rows[0].count);
}
