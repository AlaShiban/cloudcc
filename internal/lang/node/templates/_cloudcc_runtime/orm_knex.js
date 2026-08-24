// Relational database backed by RDS, via Knex.

import knex from "knex";

import { parts, password } from "./orm_url.js";

export function connect(id) {
  const { host, port, user, database, mysql } = parts(id);
  const secret = password(id);
  return knex({
    client: mysql ? "mysql2" : "pg",
    // Knex accepts an async connection factory, so the managed credential is
    // fetched on first use rather than forcing connect() to be async.
    connection: async () => ({ host, port, user, database, password: await secret() }),
  });
}
