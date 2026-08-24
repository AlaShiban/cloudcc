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
WORK="${CC_E2E_WORKDIR:-$(mktemp -d "${TMPDIR:-/tmp}/cc-e2e-XXXXXX")}"
OUT="$WORK/compiled"
STACK="ministack"
KEEP="${CC_E2E_KEEP:-0}"

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

log "building cc"
( cd "$REPO_ROOT" && go build -o "$WORK/cc" ./cmd/cc )

log "compiling examples/$EXAMPLE"
"$WORK/cc" "$REPO_ROOT/examples/$EXAMPLE" -o "$OUT"

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

APP_NAME="$(grep -E '^app:' "$OUT/cc.yaml" | head -1 | awk '{print $2}')"

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

if skip_unless_service apigatewayv2; then
  aws_local apigatewayv2 get-apis | grep -q "$APP_NAME" \
    || warn "no HTTP API matching $APP_NAME was found; the emulator may not implement apigatewayv2 fully"
  pass "L4 apigatewayv2: probed"
fi

# --------------------------------------------------------- L5: functional

log "wiring the compiled application from stack outputs"
eval "$(pulumi stack output --json --stack "$STACK" \
        | jq -r 'to_entries[] | select(.key | startswith("CC_")) | "export \(.key)=\(.value|@sh)"')"

[ -n "${CC_KV_PETSBYOWNER_TABLE:-}" ] || fail "the stack did not export CC_KV_PETSBYOWNER_TABLE"
log "table: $CC_KV_PETSBYOWNER_TABLE"

UNIT_DIR="$OUT/build/main"
[ -d "$UNIT_DIR" ] || UNIT_DIR="$OUT/main"

log "starting the compiled application against the emulator"
(
  cd "$UNIT_DIR"
  CC_AWS_ENDPOINT_URL="$MINISTACK_ENDPOINT" \
  PYTHONPATH="$UNIT_DIR" \
  uv run --quiet --with fastapi --with uvicorn --with boto3 \
    python -m uvicorn app:app --host 127.0.0.1 --port 8099 --log-level warning
) &
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
COUNT="$(aws_local dynamodb scan --table-name "$CC_KV_PETSBYOWNER_TABLE" | jq -r '.Count')"
[ "$COUNT" = "1" ] || fail "expected exactly one item in $CC_KV_PETSBYOWNER_TABLE, found $COUNT"
pass "L5 the write reached DynamoDB"

curl -sf -X DELETE http://127.0.0.1:8099/pets/1 >/dev/null || fail "DELETE /pets/1 failed"
COUNT="$(aws_local dynamodb scan --table-name "$CC_KV_PETSBYOWNER_TABLE" | jq -r '.Count')"
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
