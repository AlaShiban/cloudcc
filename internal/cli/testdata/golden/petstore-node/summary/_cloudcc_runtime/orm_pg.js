// Relational database backed by RDS, via pg.
//
// One module per client library, each importing exactly one package, so a
// bundle carries pg or knex but never both.

import { Pool } from "pg";

import { credentials, parts } from "./orm_url.js";

export function connect(id) {
  const { host, port, user, database } = parts(id);
  // pg accepts a function for `password`, which is what lets the managed
  // credential be fetched lazily without making connect() async. When there is
  // no password to give, the key is absent rather than empty -- pg treats an
  // empty string as a credential and fails, where absence lets it fall back.
  return new Pool({ host, port, user, database, ...credentials(id) });
}
