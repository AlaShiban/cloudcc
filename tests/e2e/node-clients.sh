#!/usr/bin/env bash
# Runs the injected Node runtime shims against real servers.
#
# The unit tests prove the shims compile, that the right packages land in a
# unit's manifest, and that unused ones stay out of the bundle. None of that
# proves any of them can actually talk to anything -- and each rests on a
# non-obvious property of its client library:
#
#   * ioredis is usable immediately after construction, with no connect step
#   * node-redis queues commands once connect() has been *called*, even if the
#     promise is never awaited
#   * pg accepts a function for `password`, so the managed credential can be
#     fetched lazily
#   * knex accepts an async connection factory, for the same reason
#
# Those four are what let connect() stay synchronous, which is what keeps a
# compiled binding the same shape as an uncompiled one. If any of them stops
# being true, connect() has to become async and every call on a persisted
# client changes meaning. That is worth a test that actually connects.
#
# Usage:
#   ./tests/e2e/node-clients.sh
set -euo pipefail

source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

REDIS_PORT="${CLOUDCC_VERIFY_REDIS_PORT:-6399}"
PG_PORT="${CLOUDCC_VERIFY_PG_PORT:-55432}"
REDIS_NAME=cloudcc-verify-redis
PG_NAME=cloudcc-verify-pg
WORK="$(mktemp -d "${TMPDIR:-/tmp}/cloudcc-nodeclients-XXXXXX")"

cleanup() {
  docker rm -f "$REDIS_NAME" "$PG_NAME" >/dev/null 2>&1 || true
  [ "${CLOUDCC_E2E_KEEP:-0}" = "1" ] || rm -rf "$WORK"
}
trap cleanup EXIT

log "starting real servers"
docker rm -f "$REDIS_NAME" "$PG_NAME" >/dev/null 2>&1 || true
docker run -d --name "$REDIS_NAME" -p "$REDIS_PORT:6379" redis:7-alpine >/dev/null
docker run -d --name "$PG_NAME" -p "$PG_PORT:5432" \
  -e POSTGRES_PASSWORD=secretpw -e POSTGRES_USER=ccadmin -e POSTGRES_DB=shop \
  postgres:16-alpine >/dev/null

log "installing the client libraries the shims import"
mkdir -p "$WORK/_cloudcc_runtime"
cp "$REPO_ROOT"/internal/lang/node/templates/_cloudcc_runtime/*.js "$WORK/_cloudcc_runtime/"
cat > "$WORK/package.json" <<'JSON'
{
  "name": "cloudcc-node-client-check",
  "private": true,
  "type": "module",
  "dependencies": {
    "ioredis": "^5.4.0",
    "redis": "^4.7.0",
    "pg": "^8.13.0",
    "knex": "^3.1.0",
    "@aws-sdk/client-secrets-manager": "^3.700.0"
  }
}
JSON
( cd "$WORK" && npm install --silent --no-audit --no-fund >/dev/null )

# The shims read their endpoints from the environment the compiler binds. The
# names here must match sanitize.EnvVar's spelling, which is what the compiler
# emits; getting one wrong shows up as a missing-variable error rather than a
# silent pass.
cat > "$WORK/check.mjs" <<'JS'
import * as ioredisShim from "./_cloudcc_runtime/redis_ioredis.js";
import * as noderedisShim from "./_cloudcc_runtime/redis_node.js";
import * as pgShim from "./_cloudcc_runtime/orm_pg.js";
import * as knexShim from "./_cloudcc_runtime/orm_knex.js";

let failures = 0;
const ok = (n) => console.log(`  ok    ${n}`);
const bad = (n, e) => { failures++; console.log(`  FAIL  ${n}: ${e}`); };

/** connect() must hand back a client, never a promise. */
function assertNotPromise(name, value) {
  if (value && typeof value.then === "function") {
    throw new Error(`${name} returned a Promise; the compiled binding would not match the uncompiled one`);
  }
  return value;
}

try {
  const c = assertNotPromise("redis_ioredis.connect", ioredisShim.connect("cache"));
  await c.set("k", "v");
  if ((await c.get("k")) !== "v") throw new Error("round trip failed");
  ok("redis_ioredis.js connects and round-trips");
  c.disconnect();
} catch (e) { bad("redis_ioredis.js", e.message); }

try {
  const c = assertNotPromise("redis_node.connect", noderedisShim.connect("cache"));
  await c.set("k2", "v2");
  if ((await c.get("k2")) !== "v2") throw new Error("round trip failed");
  ok("redis_node.js connects and round-trips");
  await c.quit();
} catch (e) { bad("redis_node.js", e.message); }

try {
  const pool = assertNotPromise("orm_pg.connect", pgShim.connect("shop"));
  const r = await pool.query("select 1 as n");
  if (r.rows[0].n !== 1) throw new Error("unexpected result");
  ok("orm_pg.js connects and queries");
  await pool.end();
} catch (e) { bad("orm_pg.js", e.message); }

try {
  const db = assertNotPromise("orm_knex.connect", knexShim.connect("shop"));
  const r = await db.raw("select 1 as n");
  if (Number(r.rows[0].n) !== 1) throw new Error("unexpected result");
  ok("orm_knex.js connects and queries");
  await db.destroy();
} catch (e) { bad("orm_knex.js", e.message); }

process.exit(failures === 0 ? 0 : 1);
JS

log "waiting for the servers"
"$REPO_ROOT/hack/wait-for.sh" "$REDIS_PORT" 2>/dev/null || sleep 5

log "running the shims against them"
(
  cd "$WORK"
  export CLOUDCC_REDIS_CACHE_ENDPOINT=127.0.0.1
  export CLOUDCC_REDIS_CACHE_PORT="$REDIS_PORT"
  # Two credential shapes, both without a managed secret. The password in the
  # URL is the branch that caught a real bug: the shim used to hand pg an empty
  # string here, which Postgres rejects outright.
  export CLOUDCC_ORM_SHOP_URL="postgresql://ccadmin:secretpw@127.0.0.1:$PG_PORT/shop"
  node check.mjs

  log "again, with the credential left to the driver to resolve"
  export CLOUDCC_ORM_SHOP_URL="postgresql://ccadmin@127.0.0.1:$PG_PORT/shop"
  export PGPASSWORD=secretpw
  node check.mjs
)

pass "every Node client shim talks to a real server, and none of them returns a promise"
