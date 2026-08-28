#!/usr/bin/env bash
# End-to-end integration test against an AWS emulator.
#
# Layer 4 (provisioning): compile an example, `pulumi up`, and assert the
# resources exist through the AWS CLI.
# Layer 5 (functional): run the compiled application against those resources
# and assert both the HTTP responses and the resulting datastore state.
#
# Nothing here talks to real AWS. Every assertion goes through
# $CLOUDCC_EMULATOR_ENDPOINT.
set -euo pipefail

source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

EXAMPLE="${1:-petstore}"
# The unit that serves HTTP. Most examples leave it undeclared and get "main";
# one that names its units has to say which one to drive.
UNIT="${2:-main}"
WORK="${CLOUDCC_E2E_WORKDIR:-$(mktemp -d "${TMPDIR:-/tmp}/cloudcc-e2e-XXXXXX")}"
OUT="$WORK/compiled"
STACK="local"
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
log "emulator: $CLOUDCC_EMULATOR_ENDPOINT"
log "workdir:  $WORK"

# ---------------------------------------------------------------- compile

log "building cloudcc"
( cd "$REPO_ROOT" && go build -o "$WORK/cloudcc" ./cmd/cloudcc )

# Whatever the example needs that the emulator provisions but does not run:
# a Postgres behind its RDS instance, a Redis behind its ElastiCache cluster.
SCENARIO="$REPO_ROOT/tests/e2e/scenarios/$EXAMPLE.json"
if [ -f "$SCENARIO" ]; then
  ensure_engines "$SCENARIO"
  reset_engines "$SCENARIO"
fi

log "compiling examples/$EXAMPLE"
"$WORK/cloudcc" "$REPO_ROOT/examples/$EXAMPLE" -o "$OUT"

# out_dir holds a folder per application, so `-o` names the shared root and the
# artefacts are one level down. Everything below reads the app's own directory.
OUT="$(app_out "$OUT" "$REPO_ROOT/examples/$EXAMPLE")"

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
# Built for the runtime the emulator actually uses, which is not the one a real
# deployment wants and not this machine's either: the emulator runs Lambda
# containers of whatever image it has, with whatever libc and architecture that
# brings. A bundle built for the default x86_64-manylinux2014 installs wheels
# whose compiled extensions cannot be imported there, and the failure names the
# wrong thing entirely -- "No module named 'psycopg2._psycopg'", which reads as
# a missing dependency rather than a wrong platform.
#
# load.sh and nomnom.sh have done this for a while; this harness had not, and
# nothing caught it because no unit it deploys carried a compiled extension
# until petstore-multi's worker gained a database.
if TARGET="$(emulator_python_target)"; then
  export CLOUDCC_PYTHON_PLATFORM="${TARGET%% *}" CLOUDCC_PYTHON_VERSION="${TARGET##* }"
  log "emulator runtime: $CLOUDCC_PYTHON_PLATFORM, python $CLOUDCC_PYTHON_VERSION"
fi
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

# --parallel 1: emulators stand services up in-process as they are asked for,
# and two of the same kind arriving together can collide in ways the real API
# never does -- two RDS instances generate their TLS certificates at the same
# moment and one fails with `[SSL] PEM lib`, reaching Pulumi as state `error`
# with no reason. Timing dependent, so it looks like a flaky test.
log "pulumi up"
pulumi up -y --parallel 1 --stack "$STACK" --non-interactive
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
export_engine_bindings_local
seed_secrets "$(pulumi stack output --json --stack "$STACK")"

[ -n "${CLOUDCC_KV_PETSBYOWNER_TABLE:-}" ] || fail "the stack did not export CLOUDCC_KV_PETSBYOWNER_TABLE"
log "table: $CLOUDCC_KV_PETSBYOWNER_TABLE"

# Which directory to serve the unit from depends on the language, and the
# manifest is what says which.
#
# A Node unit's build/main holds only esbuild's bundled index.mjs, which
# exports a Lambda handler rather than the app -- so the unit directory, with
# its node_modules and its entry module, is the thing to run. A Python unit's
# build/main is the unpacked bundle and is exactly right.
#
# For a Python unit it is the compiled *source* directory, not build/. The
# bundle under build/ carries wheels resolved for the deployment target --
# Linux, and a different libc and architecture than a developer's machine --
# which is right for what is deployed and unimportable here. What this test is
# for is that the compiled code runs unchanged; the host's own dependencies
# come from uv, exactly as uvicorn itself does.
if [ -f "$OUT/$UNIT/package.json" ]; then
  UNIT_DIR="$OUT/$UNIT"
elif [ -d "$OUT/$UNIT" ]; then
  UNIT_DIR="$OUT/$UNIT"
else
  UNIT_DIR="$OUT/build/$UNIT"
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
// Written by tests/e2e/provisioning.sh. Serves the compiled unit over HTTP so the
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
    exec env CLOUDCC_AWS_ENDPOINT_URL="$CLOUDCC_EMULATOR_ENDPOINT" \
      node cloudcc_e2e_serve.mjs
  ) &
else
  # exec, for the same reason as above -- this branch leaked a uvicorn on every
  # run before, which only went unnoticed because CI starts from a clean host.
  (
    cd "$UNIT_DIR"
    exec env CLOUDCC_AWS_ENDPOINT_URL="$CLOUDCC_EMULATOR_ENDPOINT" \
      PYTHONPATH="$UNIT_DIR" \
      uv run --quiet $(py_run_deps "$UNIT_DIR") \
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

# The subscriber, if this application has one.
#
# The PUT above published an event. In a multi-unit application that wakes a
# second, deployed unit, which writes to its own store -- and nothing in this
# harness went anywhere near that unit. petstore-multi's worker was never
# invoked by any test at all, which is how four calls to a method its client
# does not have survived in the examples.
#
# Whether there *is* a subscriber comes from the IR, not from the stack.
#
# This used to read "an application that exported a file-store bucket has a unit
# writing to one", which is not the same claim and is not true: examples/mixed
# has a bucket its own API writes to on demand, and no topic at all. Adding that
# bucket made this step wait fifty seconds for an event nobody published and
# then blame the handler -- a failure message pointing at a unit that does not
# subscribe to anything.
#
# The compiler already knows. A `subscribes` edge is the precondition for the
# sentence below to mean anything.
SUBSCRIBERS="$("$WORK/cloudcc" --dump-ir "$REPO_ROOT/examples/$EXAMPLE" 2>/dev/null \
               | jq -r '[.edges[]? | select(.kind=="subscribes")] | length' 2>/dev/null || echo 0)"
FS_BUCKET="$(pulumi stack output --json --stack "$STACK" \
             | jq -r 'to_entries[] | select(.key | test("^CLOUDCC_FS_.*_BUCKET$")) | .value' \
             | head -1)"
if [ "${SUBSCRIBERS:-0}" -eq 0 ]; then
  log "no unit subscribes to a topic in this application; nothing to wait for"
elif [ -n "$FS_BUCKET" ] && skip_unless_service s3; then
  log "waiting for the subscriber to write to $FS_BUCKET"
  OBJECTS=0
  for _ in $(seq 1 20); do
    OBJECTS="$(aws_local s3api list-objects-v2 --bucket "$FS_BUCKET" --output json 2>/dev/null \
               | jq -r '(.Contents // []) | length')"
    [ "${OBJECTS:-0}" -ge 1 ] && break
    sleep 2
  done
  [ "${OBJECTS:-0}" -ge 1 ] \
    || fail "nothing reached $FS_BUCKET. The published event should have woken a
  subscribing unit, which writes to its own store -- so either the message was
  not delivered, or the handler raised."
  pass "L5 the published event reached a subscriber, which wrote to its own store"
fi

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
