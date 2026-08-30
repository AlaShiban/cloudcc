// Relational database backed by RDS, via Knex.

import knex from "knex";

import { credentials, parts } from "./orm_url.js";
import { wrap } from "./trace.js";

export function connect(id) {
  const { host, port, user, database, mysql } = parts(id);
  const auth = credentials(id);
  return wrap(knex({
    client: mysql ? "mysql2" : "pg",
    // Knex accepts an async connection factory, so the managed credential is
    // fetched on first use rather than forcing connect() to be async.
    connection: async () => ({
      host,
      port,
      user,
      database,
      ...(typeof auth.password === "function" ? { password: await auth.password() } : auth),
    }),
  }), "orm", id);
}
