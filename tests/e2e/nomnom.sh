#!/usr/bin/env bash
# End-to-end integration test for the nomnom example against an AWS emulator.
#
# nomnom is six execution units that talk to each other two ways -- awaited
# calls that compile to Lambda invocations, and published messages that compile
# to SNS -- so this harness exists to prove the parts that cannot be checked by
# reading the generated project:
#
#   L4  every unit and every store was really provisioned
#   L5a a deployed unit answers a call envelope, and the call persists
#   L5b a caller reaches a callee over the wire and gets the value back
#   L5c a published message wakes a subscriber, which itself calls a third
#       unit -- a Lambda invoking a Lambda inside the emulator -- and every
#       store along that chain ends up in the right state
#
# L5c is the one worth having. It is the whole point of the example, it spans
# five of the six units, and nothing about it is visible from the source of any
# one of them.
#
# Nothing here talks to real AWS. Every assertion goes through
# $MINISTACK_ENDPOINT.
set -euo pipefail

source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

EXAMPLE="nomnom"
UNIT="storefront"
WORK="${CLOUDCC_E2E_WORKDIR:-$(mktemp -d "${TMPDIR:-/tmp}/cloudcc-nomnom-XXXXXX")}"
OUT="$WORK/compiled"
STACK="ministack"
KEEP="${CLOUDCC_E2E_KEEP:-0}"
PORT=8098

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
    log "workdir kept at $WORK"
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
OUT="$(app_out "$OUT" "$REPO_ROOT/examples/$EXAMPLE")"

[ -f "$OUT/index.ts" ] || fail "no index.ts was generated"
pass "compiled"

# The remote boundary is a compile-time property and this is the cheapest place
# to check it: if the storefront's bundle carried another unit's module, the
# split would be cosmetic -- the caller would still be granted that unit's
# permissions and still be carrying its dependencies.
for foreign in pricing inventory tracking dispatch notify; do
  if [ -f "$OUT/$UNIT/nomnom/$foreign.py" ]; then
    fail "the storefront bundle contains nomnom/$foreign.py; the remote boundary did not cut the import closure"
  fi
done
pass "each unit's bundle stops at the remote boundary"

# ---------------------------------------------------------------- package

log "installing node dependencies"
( cd "$OUT" && npm install --silent --no-audit --no-fund )

log "type-checking the generated project"
( cd "$OUT" && ./node_modules/.bin/tsc --noEmit ) || fail "the generated TypeScript does not type-check"
pass "tsc --noEmit"

# What a real deploy gets is x86_64 and the declared runtime, and neither
# variable is set there. The emulator honours neither, so it is asked.
if TARGET="$(emulator_python_target)"; then
  export CLOUDCC_PYTHON_PLATFORM="${TARGET%% *}"
  export CLOUDCC_PYTHON_VERSION="${TARGET##* }"
else
  warn "could not determine the emulator's Lambda target; packaging for the deployed defaults"
fi
log "packaging six execution units for ${CLOUDCC_PYTHON_PLATFORM:-the deployed default} / python ${CLOUDCC_PYTHON_VERSION:-declared}"
( cd "$OUT" && ./bin/package.sh )
[ "$(ls "$OUT"/build/*.zip 2>/dev/null | wc -l | tr -d ' ')" = "6" ] \
  || fail "expected six deployment artefacts, found $(ls "$OUT"/build/*.zip 2>/dev/null | wc -l | tr -d ' ')"
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

if skip_unless_service lambda; then
  for unit in storefront pricing inventory dispatch tracking notify; do
    aws_local lambda get-function --function-name "nomnom-$unit" >/dev/null 2>&1 \
      || fail "execution unit $unit was not provisioned as a Lambda function"
  done
  pass "L4 lambda: all six units provisioned"
fi

if skip_unless_service dynamodb; then
  TABLES="$(aws_local dynamodb list-tables --output json | jq -r '.TableNames[]')"
  for store in menuPrices reservations orders assignments trackingEvents; do
    printf '%s\n' "$TABLES" | grep -qi "$store" \
      || fail "no DynamoDB table was provisioned for $store"
  done
  pass "L4 dynamodb: all five tables provisioned"
fi

if skip_unless_service sns; then
  for topic in orderPlaced courierAssigned; do
    aws_local sns list-topics | grep -qi "$topic" || fail "no SNS topic for $topic"
  done
  pass "L4 sns: both topics provisioned"
fi

# --------------------------------------------- L4b: every unit can start
#
# A unit's environment is derived from its edges, and a binding it needs but has
# no edge for is missing. Nothing catches that until the module that reads it is
# imported -- which happens on the deployed unit's first invocation, and nowhere
# else. Running a unit locally hides it completely, because a local run is
# handed every stack output at once.
#
# The probe is a call for a function that does not exist. A unit that started
# answers with an error *envelope*; a unit that could not import its own modules
# fails to start at all, which is a different and much louder failure.
#
# This found a real one: tracking imports the module declaring both topics but
# only subscribes to one, so it was given one ARN and died on the other.
log "probing every deployed unit for a clean start"
for unit in storefront pricing inventory dispatch tracking notify; do
  aws_local lambda invoke \
    --function-name "nomnom-$unit" \
    --cli-binary-format raw-in-base64-out \
    --payload '{"cloudcc_call":{"function":"__cloudcc_probe__","args":[],"kwargs":{}}}' \
    "$WORK/probe-$unit.json" > "$WORK/probe-$unit.meta" 2>&1 \
    || fail "could not invoke nomnom-$unit"

  if grep -q '"FunctionError"' "$WORK/probe-$unit.meta"; then
    fail "execution unit $unit does not start:
  $(cat "$WORK/probe-$unit.json")"
  fi
  jq -e 'has("cloudcc_error")' "$WORK/probe-$unit.json" >/dev/null \
    || fail "$unit answered a probe for a function it does not have: $(cat "$WORK/probe-$unit.json")"
done
pass "L4b all six units import their modules and answer"

# The storefront's job is HTTP, and Mangum is initialised at import, so the
# probe above does not cover it.
log "probing the deployed storefront over HTTP"
aws_local lambda invoke --function-name "nomnom-storefront" \
  --cli-binary-format raw-in-base64-out \
  --payload '{"version":"2.0","routeKey":"$default","rawPath":"/health","rawQueryString":"","headers":{},"requestContext":{"http":{"method":"GET","path":"/health","protocol":"HTTP/1.1","sourceIp":"127.0.0.1"},"stage":"$default","requestId":"probe","apiId":"probe","domainName":"probe","accountId":"000000000000","time":"","timeEpoch":0},"isBase64Encoded":false}' \
  "$WORK/http-probe.json" >/dev/null
jq -e '.statusCode == 200' "$WORK/http-probe.json" >/dev/null \
  || fail "the deployed storefront did not answer GET /health: $(cat "$WORK/http-probe.json")"
pass "L4b the deployed storefront serves HTTP"

# ---------------------------------------------------- wire from the stack

log "reading stack outputs"
eval "$(pulumi stack output --json --stack "$STACK" \
        | jq -r 'to_entries[] | select(.key | startswith("CLOUDCC_")) | "export \(.key)=\(.value|@sh)"')"

for required in CLOUDCC_KV_ORDERS_TABLE CLOUDCC_KV_MENUPRICES_TABLE \
                CLOUDCC_KV_RESERVATIONS_TABLE CLOUDCC_KV_ASSIGNMENTS_TABLE \
                CLOUDCC_KV_TRACKINGEVENTS_TABLE CLOUDCC_FS_NOTIFICATIONS_BUCKET \
                CLOUDCC_UNIT_PRICING_FUNCTION CLOUDCC_UNIT_INVENTORY_FUNCTION \
                CLOUDCC_UNIT_TRACKING_FUNCTION; do
  [ -n "${!required:-}" ] || fail "the stack did not export $required"
done
# A caller is handed the callee's function name the same way it is handed a
# table name: from an edge in the graph. If that binding were missing the
# storefront would fail on its first call with a message about an unset
# variable, which is the failure this line turns into a clear one.
pass "L4 the stack exports a function name for every unit that is called"

# ------------------------------- L5a: a deployed unit answers a call

# The call envelope is the protocol between the rpc shim and the generated
# entrypoint. Invoking it directly with the CLI proves the callee half works
# when deployed, independently of whether any caller can reach it.
log "invoking the deployed pricing unit with a call envelope"
aws_local lambda invoke \
  --function-name "$CLOUDCC_UNIT_PRICING_FUNCTION" \
  --cli-binary-format raw-in-base64-out \
  --payload '{"cloudcc_call":{"function":"set_price","args":["margherita",1500],"kwargs":{}}}' \
  "$WORK/setprice.json" >/dev/null
jq -e '.cents == 1500' "$WORK/setprice.json" >/dev/null \
  || fail "the deployed pricing unit did not answer set_price: $(cat "$WORK/setprice.json")"
pass "L5a a deployed unit answers a call envelope"

PRICE="$(aws_local dynamodb get-item --table-name "$CLOUDCC_KV_MENUPRICES_TABLE" \
          --key '{"id":{"S":"margherita"}}' | jq -r '.Item.cents.N // ""')"
[ "$PRICE" = "1500" ] || fail "the remote call did not persist: menuPrices has cents=$PRICE, expected 1500"
pass "L5a the call persisted to the callee's own store"

# An async def really was awaited. Had the entrypoint returned the coroutine
# instead, the reply would have been null and every assertion above would have
# failed in a way that pointed at the wrong thing -- so this says it plainly.
jq -e 'type == "object"' "$WORK/setprice.json" >/dev/null \
  || fail "the reply was not an object; an async def was probably not awaited"
pass "L5a the callee awaited its async def"

# ------------------------- L5b: a caller reaches a callee over the wire

if lsof -ti:$PORT >/dev/null 2>&1; then
  fail "port $PORT is already in use; a server from an earlier run is still up (lsof -ti:$PORT | xargs kill)"
fi

# The compiled *sources*, not the deployment bundle. build/ carries wheels
# resolved for the target -- Linux, and here musl -- which is correct for what
# is deployed and unimportable on this machine. What is being tested is that
# the compiled code runs unchanged, so the host supplies the host's own
# dependencies, exactly as uvicorn itself is supplied.
log "starting the compiled storefront against the emulator"
(
  cd "$OUT/$UNIT"
  exec env CLOUDCC_AWS_ENDPOINT_URL="$MINISTACK_ENDPOINT" \
    PYTHONPATH="$OUT/$UNIT" \
    uv run --quiet --with fastapi --with uvicorn --with boto3 \
      python -m uvicorn storefront:app --host 127.0.0.1 --port $PORT --log-level warning
) &
APP_PID=$!

wait_for_http "http://127.0.0.1:$PORT/health" 60 \
  || fail "the compiled storefront did not start; it should run unchanged against an emulator (D15)"
pass "L5b the compiled storefront starts with no code changes"

ORDER_ID="order-$$"
log "placing an order (this calls pricing and inventory over the wire)"
RESPONSE="$(curl -sf -X POST "http://127.0.0.1:$PORT/orders" \
  -H 'content-type: application/json' \
  -d "{\"order_id\":\"$ORDER_ID\",\"restaurant\":\"luigi\",\"items\":[{\"sku\":\"margherita\",\"qty\":2},{\"sku\":\"cola\",\"qty\":1}]}")" \
  || fail "POST /orders failed"

# 2 x 1500 (the price the earlier remote set_price wrote, not the 1200 in the
# catalogue) + 1 x 300 (from the catalogue, since nothing set it) + 349
# delivery = 3649.
echo "$RESPONSE" | jq -e '.total_cents == 3649' >/dev/null \
  || fail "the price came back wrong: $RESPONSE
  This total can only be right if the call reached the deployed pricing unit
  AND that unit read the row the earlier remote call wrote."
pass "L5b a value crossed the wire and came back correct"

echo "$RESPONSE" | jq -e '.state == "held"' >/dev/null \
  || fail "inventory did not reserve: $RESPONSE"
pass "L5b a second callee was reached in the same request"

COUNT="$(aws_local dynamodb scan --table-name "$CLOUDCC_KV_ORDERS_TABLE" | jq -r '.Count')"
[ "$COUNT" = "1" ] || fail "expected one order, found $COUNT"
STATE="$(aws_local dynamodb get-item --table-name "$CLOUDCC_KV_RESERVATIONS_TABLE" \
          --key "{\"id\":{\"S\":\"$ORDER_ID\"}}" | jq -r '.Item.state.S // ""')"
# Either is correct here, and pinning it to "held" was a race in this test
# rather than a fact about the program: publishing orderPlaced is the last
# thing the request does, and the whole downstream chain -- dispatch waking,
# calling inventory, committing -- can finish before the next line runs. What
# this assertion is for is that inventory wrote to its *own* table at all.
case "$STATE" in
  held|committed) ;;
  *) fail "the reservation is '$STATE'; inventory did not write to its own table" ;;
esac
pass "L5b both units wrote to their own stores (reservation: $STATE)"

# --------------------------- L5c: the message chain, including Lambda -> Lambda

# Placing the order published orderPlaced. Everything from here happens without
# the storefront: dispatch wakes, calls inventory over the wire from inside the
# emulator, writes an assignment and publishes courierAssigned; tracking and
# notify wake on that.
log "waiting for the message chain to settle"
for _ in $(seq 1 30); do
  COMMITTED="$(aws_local dynamodb get-item --table-name "$CLOUDCC_KV_RESERVATIONS_TABLE" \
                --key "{\"id\":{\"S\":\"$ORDER_ID\"}}" | jq -r '.Item.state.S // ""')"
  [ "$COMMITTED" = "committed" ] && break
  sleep 2
done

[ "$COMMITTED" = "committed" ] \
  || fail "the reservation is still '$COMMITTED'.
  Reaching 'committed' requires: SNS delivered orderPlaced to dispatch, and
  dispatch -- running as a Lambda -- invoked the inventory Lambda and waited for
  it. That is one execution unit calling another over the wire, with no local
  process involved."
pass "L5c a subscriber woke and called a third unit over the wire"

COURIER="$(aws_local dynamodb get-item --table-name "$CLOUDCC_KV_ASSIGNMENTS_TABLE" \
            --key "{\"id\":{\"S\":\"$ORDER_ID\"}}" | jq -r '.Item.courier.S // ""')"
[ -n "$COURIER" ] || fail "dispatch wrote no assignment"
pass "L5c dispatch persisted its assignment ($COURIER)"

for _ in $(seq 1 20); do
  TRACKED="$(aws_local dynamodb get-item --table-name "$CLOUDCC_KV_TRACKINGEVENTS_TABLE" \
              --key "{\"id\":{\"S\":\"$ORDER_ID\"}}" | jq -r '.Item.state.S // ""')"
  [ -n "$TRACKED" ] && break
  sleep 2
done
[ "$TRACKED" = "out-for-delivery" ] \
  || fail "tracking did not react to courierAssigned; state is '$TRACKED'"
pass "L5c a second message reached a second subscriber"

# notify listens to both topics and writes to a bucket rather than a table,
# which is the one store in this program that is not DynamoDB.
for _ in $(seq 1 20); do
  # Counted from Contents rather than from KeyCount: not every implementation
  # returns KeyCount, and `.KeyCount // 0` on one that does not reads as an
  # empty bucket -- which is a test that fails while the product works.
  OBJECTS="$(aws_local s3api list-objects-v2 --bucket "$CLOUDCC_FS_NOTIFICATIONS_BUCKET" \
              --prefix "$ORDER_ID" --output json 2>/dev/null | jq -r '(.Contents // []) | length')"
  [ "${OBJECTS:-0}" -ge 2 ] && break
  sleep 2
done
[ "${OBJECTS:-0}" -ge 2 ] \
  || fail "the outbox holds ${OBJECTS:-0} objects for $ORDER_ID; notify should have written one per topic"
pass "L5c the fan-out reached a third subscriber, writing to S3"

# --------------------------- L5b again: reading back through a call

STATUS="$(curl -sf "http://127.0.0.1:$PORT/orders/$ORDER_ID")" || fail "GET /orders/$ORDER_ID failed"
echo "$STATUS" | jq -e '.state == "out-for-delivery"' >/dev/null \
  || fail "the storefront did not read the state back through tracking: $STATUS"
echo "$STATUS" | jq -e --arg c "$COURIER" '.courier == $c' >/dev/null \
  || fail "the courier did not survive the round trip: $STATUS"
pass "L5b a read path crossed the wire and agreed with the store"

curl -sf -X DELETE "http://127.0.0.1:$PORT/orders/$ORDER_ID" >/dev/null || fail "DELETE /orders failed"
LEFT="$(aws_local dynamodb get-item --table-name "$CLOUDCC_KV_RESERVATIONS_TABLE" \
         --key "{\"id\":{\"S\":\"$ORDER_ID\"}}" | jq -r '.Item.id.S // ""')"
[ -z "$LEFT" ] || fail "inventory.release did not remove the reservation"
pass "L5b a write path crossed the wire"

STATUS_CODE="$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT/orders/missing")"
[ "$STATUS_CODE" = "404" ] || fail "a missing order should produce a 404, got $STATUS_CODE"
pass "L5b missing orders return 404"

kill "$APP_PID" 2>/dev/null || true
wait "$APP_PID" 2>/dev/null || true
APP_PID=""

# ------------------------------------------------------------- teardown

log "pulumi destroy"
pulumi destroy -y --stack "$STACK" --non-interactive
DESTROYED=1

if skip_unless_service lambda; then
  if aws_local lambda list-functions | grep -q "nomnom-pricing"; then
    fail "a function survived destroy"
  fi
  pass "L4 destroy removed the units"
fi

log "all nomnom integration assertions passed"
