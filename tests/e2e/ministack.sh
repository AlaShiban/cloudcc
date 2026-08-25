#!/usr/bin/env bash
# End-to-end integration test against an AWS emulator.
#
# Layer 4 (provisioning): compile an example, `pulumi up`, and assert the
# resources exist through the AWS CLI.
# Layer 5 (functional): run the compiled application against those resources
# and assert both the HTTP responses and the resulting datastore state.
#
# Nothing here talks to real AWS. Every assertion goes through
# $MINISTACK_ENDPOINT.
set -euo pipefail

source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

EXAMPLE="${1:-petstore}"
# The unit that serves HTTP. Most examples leave it undeclared and get "main";
# one that names its units has to say which one to drive.
UNIT="${2:-main}"
WORK="${CLOUDCC_E2E_WORKDIR:-$(mktemp -d "${TMPDIR:-/tmp}/cloudcc-e2e-XXXXXX")}"
OUT="$WORK/compiled"
STACK="ministack"
KEEP="${CLOUDCC_E2E_KEEP:-0}"

cleanup() {
  local status=$?
  if [ -n "${APP_PID:-}" ] && kill -0 "$APP_PID" 2>/dev/null; then
    kill "$APP_PID" 2>/dev/null || true
    wait "$APP_PID" 2>/dev/null || true
  fi
  if [ -d "$OUT" ] && [ "${DESTROYED:-0}" != "1" ] && [ "$KEEP" = "0" ]; then
    ( cd "$OUT" && pulumi destroy -y --stack "$STACK" >/dev/null 2>&1 ) || true
  fi
  if [ "$KEEP" = "0" ]; then
    rm -rf "$WORK"
  else
    log "workdir kept at $WORK (stack left up; destroy it with: cd $OUT && PULUMI_BACKEND_URL=file://$WORK/pulumi-state pulumi destroy -y -s $STACK)"
  fi
  exit $status
}
trap cleanup EXIT

require_endpoint
log "emulator: $MINISTACK_ENDPOINT"
log "workdir:  $WORK"

# ---------------------------------------------------------------- compile

log "building cloudcc"
( cd "$REPO_ROOT" && go build -o "$WORK/cloudcc" ./cmd/cloudcc )

log "compiling examples/$EXAMPLE"
"$WORK/cloudcc" "$REPO_ROOT/examples/$EXAMPLE" -o "$OUT"

[ -f "$OUT/index.ts" ]      || fail "no index.ts was generated"
[ -x "$OUT/bin/package.sh" ] || fail "bin/package.sh is missing or not executable"
pass "compiled"

# ---------------------------------------------------------------- package

log "installing node dependencies"
( cd "$OUT" && npm install --silent --no-audit --no-fund )

log "type-checking the generated project"
( cd "$OUT" && ./node_modules/.bin/tsc --noEmit ) || fail "the generated TypeScript does not type-check"
pass "tsc --noEmit"

log "packaging execution units"
( cd "$OUT" && ./bin/package.sh )
ls "$OUT"/build/*.zip >/dev/null 2>&1 || fail "package.sh produced no deployment artefact"
pass "packaged"

# ---------------------------------------------------------------- deploy

export PULUMI_BACKEND_URL="file://$WORK/pulumi-state"
mkdir -p "$WORK/pulumi-state"

cd "$OUT"
log "creating stack $STACK"
pulumi stack init "$STACK" --non-interactive >/dev/null 2>&1 || pulumi stack select "$STACK" >/dev/null
pulumi_configure_emulator "$STACK"

log "pulumi up"
pulumi up -y --stack "$STACK" --non-interactive
pass "provisioned"

# ------------------------------------------------------- L4: provisioning

APP_NAME="$(grep -E '^app:' "$OUT/cloudcc.yaml" | head -1 | awk '{print $2}')"

if skip_unless_service dynamodb; then
  aws_local dynamodb list-tables | grep -q "petsByOwner" \
    || fail "no DynamoDB table matching petsByOwner was provisioned"
  pass "L4 dynamodb: table provisioned"
fi

if skip_unless_service lambda; then
  aws_local lambda list-functions | grep -q "$APP_NAME" \
    || fail "no Lambda function matching $APP_NAME was provisioned"
  pass "L4 lambda: function provisioned"
fi

# Where logs go is configuration rather than code, so nothing in the program
# mentions it and nothing else in this harness would notice if it were dropped.
if skip_unless_service logs; then
  RETENTION="$(aws_local logs describe-log-groups \
    --log-group-name-prefix "/aws/lambda/" \
    | jq -r '[.logGroups[] | select(.retentionInDays != null) | .retentionInDays][0] // ""')"
  [ -n "$RETENTION" ] \
    || fail "no log group carried a retention policy; logging.retention_days was dropped"
  [ "$RETENTION" = "14" ] \
    || fail "log retention is $RETENTION days, expected the configured 14"
  pass "L4 logs: the configured retention reached the log group"
fi

if skip_unless_service apigatewayv2; then
  aws_local apigatewayv2 get-apis | grep -q "$APP_NAME" \
    || warn "no HTTP API matching $APP_NAME was found; the emulator may not implement apigatewayv2 fully"
  pass "L4 apigatewayv2: probed"
fi

# --------------------------------------------------------- L5: functional

log "wiring the compiled application from stack outputs"
eval "$(pulumi stack output --json --stack "$STACK" \
        | jq -r 'to_entries[] | select(.key | startswith("CLOUDCC_")) | "export \(.key)=\(.value|@sh)"')"

[ -n "${CLOUDCC_KV_PETSBYOWNER_TABLE:-}" ] || fail "the stack did not export CLOUDCC_KV_PETSBYOWNER_TABLE"
log "table: $CLOUDCC_KV_PETSBYOWNER_TABLE"

# Which directory to serve the unit from depends on the language, and the
# manifest is what says which.
#
# A Node unit's build/main holds only esbuild's bundled index.mjs, which
# exports a Lambda handler rather than the app -- so the unit directory, with
# its node_modules and its entry module, is the thing to run. A Python unit's
# build/main is the unpacked bundle and is exactly right.
if [ -f "$OUT/$UNIT/package.json" ]; then
  UNIT_DIR="$OUT/$UNIT"
elif [ -d "$OUT/build/$UNIT" ]; then
  UNIT_DIR="$OUT/build/$UNIT"
else
  UNIT_DIR="$OUT/$UNIT"
fi
[ -d "$UNIT_DIR" ] || fail "unit $UNIT has no directory in the compiled output; pass the unit name as the second argument"

# The compiled unit is served the same way whatever language it is in, but the
# server is language-specific: uvicorn for Python, and for Node a launcher this
# script writes. That asymmetry is only apparent -- uvicorn is a harness-supplied
# runner too, not part of the application, and neither one is in the bundle.
# A server left over from an earlier run answers the health check and every
# assertion after it, silently testing the wrong process against a stack that
# no longer exists. That produced a genuinely baffling failure once; refusing
# to start is much easier to read than the results of not doing so.
if lsof -ti:8099 >/dev/null 2>&1; then
  fail "port 8099 is already in use; a server from an earlier run is still up (lsof -ti:8099 | xargs kill)"
fi

# Which module holds the application, and what the binding is called, are
# facts the compiler already worked out -- so ask it rather than assume. This
# branch used to run `uvicorn app:app`, which is right for petstore and wrong
# for every program whose entry is not called app.py.
PY_ENTRY="$("$WORK/cloudcc" --dump-ir "$REPO_ROOT/examples/$EXAMPLE" 2>/dev/null \
  | jq -r --arg unit "$UNIT" \
      '[.intents[] | select(.key.kind=="execution_unit") | select(.payload.id==$unit)][0]
       | "\(.payload.entrypoints[0]):\(.payload.asgi_app)"' 2>/dev/null || true)"
if [ -z "$PY_ENTRY" ] || [ "$PY_ENTRY" = "null:null" ]; then
  PY_ENTRY="app.py:app"
fi
PY_TARGET="$(printf '%s' "${PY_ENTRY%%:*}" | sed 's/\.py$//' | tr '/' '.'):${PY_ENTRY##*:}"

log "starting the compiled application against the emulator"
if [ -f "$UNIT_DIR/package.json" ]; then
  # The unit's manifest names its entry module, and the generated Lambda entry
  # takes the app off the same export. Reading it from the manifest keeps this
  # from hardcoding a filename the compiler chose.
  ENTRY="$(jq -r '.main // "index.js"' "$UNIT_DIR/package.json")"
  cat > "$UNIT_DIR/cloudcc_e2e_serve.mjs" <<SERVE
// Written by tests/e2e/ministack.sh. Serves the compiled unit over HTTP so the
// same functional assertions can be made against it as against a Python one.
const m = await import("./$ENTRY");
const app = m.app ?? m.default;
if (!app) {
  console.error("the compiled entry exports neither app nor default");
  process.exit(1);
}
app.listen(8099, "127.0.0.1", () => console.log("listening"));
SERVE
  # exec, so that $! is the server itself: backgrounding a subshell makes $!
  # the subshell's pid, and killing that leaves the server running to poison
  # the next run.
  (
    cd "$UNIT_DIR"
    exec env CLOUDCC_AWS_ENDPOINT_URL="$MINISTACK_ENDPOINT" \
      node cloudcc_e2e_serve.mjs
  ) &
else
  # exec, for the same reason as above -- this branch leaked a uvicorn on every
  # run before, which only went unnoticed because CI starts from a clean host.
  (
    cd "$UNIT_DIR"
    exec env CLOUDCC_AWS_ENDPOINT_URL="$MINISTACK_ENDPOINT" \
      PYTHONPATH="$UNIT_DIR" \
      uv run --quiet --with fastapi --with uvicorn --with boto3 \
        python -m uvicorn "$PY_TARGET" --host 127.0.0.1 --port 8099 --log-level warning
  ) &
fi
APP_PID=$!

wait_for_http "http://127.0.0.1:8099/health" 45 \
  || fail "the compiled application did not start; it should run unchanged against an emulator (D15)"
pass "L5 the compiled application starts with no code changes"

log "exercising the API"
curl -sf -X PUT http://127.0.0.1:8099/pets/1 \
     -H 'content-type: application/json' \
     -d '{"name":"rex","species":"dog"}' >/dev/null \
  || fail "PUT /pets/1 failed"

curl -sf http://127.0.0.1:8099/pets/1 | jq -e '.name == "rex"' >/dev/null \
  || fail "GET /pets/1 did not return the stored pet"
pass "L5 round-trip through the API"

log "asserting the datastore state"
COUNT="$(aws_local dynamodb scan --table-name "$CLOUDCC_KV_PETSBYOWNER_TABLE" | jq -r '.Count')"
[ "$COUNT" = "1" ] || fail "expected exactly one item in $CLOUDCC_KV_PETSBYOWNER_TABLE, found $COUNT"
pass "L5 the write reached DynamoDB"

curl -sf -X DELETE http://127.0.0.1:8099/pets/1 >/dev/null || fail "DELETE /pets/1 failed"
COUNT="$(aws_local dynamodb scan --table-name "$CLOUDCC_KV_PETSBYOWNER_TABLE" | jq -r '.Count')"
[ "$COUNT" = "0" ] || fail "expected the item to be deleted, found $COUNT"
pass "L5 the delete reached DynamoDB"

# No -f here: it makes curl exit non-zero and print nothing on a 4xx, which is
# exactly the status this assertion is trying to observe.
STATUS="$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8099/pets/missing)"
[ "$STATUS" = "404" ] || fail "a missing pet should produce a 404, got $STATUS"
pass "L5 missing keys return 404"

kill "$APP_PID" 2>/dev/null || true
wait "$APP_PID" 2>/dev/null || true
APP_PID=""

# ------------------------------------------------------------- teardown

log "pulumi destroy"
pulumi destroy -y --stack "$STACK" --non-interactive
DESTROYED=1

if skip_unless_service dynamodb; then
  if aws_local dynamodb list-tables | grep -q "petsByOwner"; then
    fail "the table survived destroy"
  fi
  pass "L4 destroy removed the table"
fi

log "all integration assertions passed"
