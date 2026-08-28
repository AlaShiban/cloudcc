#!/usr/bin/env bash
# Differential test: the same generated program, run twice.
#
#   A. uncompiled -- the program as written, against the SDK's local emulations
#   B. compiled   -- the same program after cloudcc rewrote it, against real
#                    AWS services in the emulator
#
# The same request scenario is replayed against both, and every response must
# match. This is the actual correctness guarantee the compiler owes: that
# rewriting a program preserves what it does. Everything else -- the IR, the
# generated TypeScript -- is a means to that end.
#
# Usage:
#   ./tests/e2e/differential.sh [seed ...]
set -euo pipefail

source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

LANGUAGE="${CLOUDCC_DIFF_LANG:-python}"

SEEDS=("$@")
if [ ${#SEEDS[@]} -eq 0 ]; then
  read -r -a SEEDS <<< "${CLOUDCC_DIFF_SEEDS:-1 2 3}"
fi

WORK="${CLOUDCC_DIFF_WORKDIR:-$(mktemp -d "${TMPDIR:-/tmp}/cloudcc-diff-XXXXXX")}"
KEEP="${CLOUDCC_E2E_KEEP:-0}"
PORT_A=8101
PORT_B=8102

APP_PID=""
CURRENT_OUT=""

stop_app() {
  if [ -n "$APP_PID" ] && kill -0 "$APP_PID" 2>/dev/null; then
    kill "$APP_PID" 2>/dev/null || true
    wait "$APP_PID" 2>/dev/null || true
  fi
  APP_PID=""
}

cleanup() {
  local status=$?
  stop_app
  if [ -n "$CURRENT_OUT" ] && [ -d "$CURRENT_OUT" ]; then
    "$WORK/cloudcc" deploy "$CURRENT_OUT/../src" -o "$CURRENT_OUT" \
      --stack local --destroy >/dev/null 2>&1 || true
  fi
  [ "$KEEP" = "0" ] && rm -rf "$WORK" || log "workdir kept at $WORK"
  exit $status
}
trap cleanup EXIT

require_endpoint
log "emulator: $CLOUDCC_EMULATOR_ENDPOINT"
log "seeds:    ${SEEDS[*]}"

log "building cloudcc"
( cd "$REPO_ROOT" && go build -o "$WORK/cloudcc" ./cmd/cloudcc )

# serve starts a program and waits for it to answer.
#   serve <dir> <target> <port> <label> [extra uv args...]
#
# For Python the target is module:app and uvicorn does the serving. Node has no
# uvicorn, so a launcher is written next to the program: the same arrangement,
# since uvicorn is no more part of the application than that launcher is.
serve() {
  local dir="$1" target="$2" port="$3" label="$4"
  shift 4

  if [ "$LANGUAGE" = "node" ]; then
    local entry="${target%%:*}" appvar="${target##*:}"
    cat > "$dir/cloudcc_serve.mjs" <<SERVE
const m = await import("./$entry");
const app = m.$appvar ?? m.default;
if (!app) {
  console.error("the entry exports neither $appvar nor default");
  process.exit(1);
}
app.listen($port, "127.0.0.1");
SERVE
    ( cd "$dir" && exec node cloudcc_serve.mjs ) &
  else
    ( cd "$dir" && PYTHONPATH="$dir" exec uv run --quiet "$@" \
        python -m uvicorn "$target" --host 127.0.0.1 --port "$port" --log-level error ) &
  fi
  APP_PID=$!
  if ! wait_for_http "http://127.0.0.1:$port/health" 60; then
    fail "the $label program did not start on port $port"
  fi
}

# reset_local_table gives the uncompiled run an empty table to talk to.
#
# The program as written holds a real client now -- a boto3 Table, or a
# DynamoDBClient -- rather than a class the SDK supplied, so "run it as written"
# means running it against a real DynamoDB. The emulator is already up for the
# compiled half, so it serves both, with the *local* table name the program
# wrote rather than the physical one the compiler chose.
#
# Dropping and recreating it per seed matters: the two halves talk to different
# tables, so an item left behind by an earlier seed would show up in one run's
# listing and not the other, and read as a behavioural difference.
LOCAL_TABLE="items"

reset_local_table() {
  aws_local dynamodb delete-table --table-name "$LOCAL_TABLE" >/dev/null 2>&1 || true
  aws_local dynamodb create-table --table-name "$LOCAL_TABLE" \
    --attribute-definitions AttributeName=id,AttributeType=S \
    --key-schema AttributeName=id,KeyType=HASH \
    --billing-mode PAY_PER_REQUEST >/dev/null \
    || fail "could not create the local $LOCAL_TABLE table in the emulator"
}

# replay issues the scenario and writes one normalised line per step, so the
# two runs can be compared with a plain diff.
replay() {
  local port="$1" manifest="$2" out="$3"
  : > "$out"

  local count
  count="$(jq '.scenario | length' "$manifest")"
  for ((i = 0; i < count; i++)); do
    local method path body status raw
    method="$(jq -r ".scenario[$i].Method" "$manifest")"
    path="$(jq -r ".scenario[$i].Path" "$manifest")"
    body="$(jq -r ".scenario[$i].Body" "$manifest")"

    local tmp="$WORK/response.$$"
    if [ -n "$body" ] && [ "$body" != "null" ]; then
      status="$(curl -s -o "$tmp" -w '%{http_code}' -X "$method" \
        -H 'content-type: application/json' -d "$body" \
        "http://127.0.0.1:$port$path")"
    else
      status="$(curl -s -o "$tmp" -w '%{http_code}' -X "$method" \
        "http://127.0.0.1:$port$path")"
    fi

    # Sort object keys so an incidental ordering difference is not mistaken
    # for a behavioural one. A non-JSON body is compared verbatim.
    if raw="$(jq -S -c . < "$tmp" 2>/dev/null)"; then
      printf '%s %s -> %s %s\n' "$method" "$path" "$status" "$raw" >> "$out"
    else
      printf '%s %s -> %s %s\n' "$method" "$path" "$status" "$(tr -d '\n' < "$tmp")" >> "$out"
    fi
    rm -f "$tmp"
  done
}

failures=0

for seed in "${SEEDS[@]}"; do
  log "================ seed $seed ================"
  case_dir="$WORK/case-$seed"
  src="$case_dir/src"
  out="$case_dir/out"
  mkdir -p "$src"

  manifest="$case_dir/manifest.json"
  ( cd "$REPO_ROOT" && go run ./internal/fuzz/cmd/genprogram \
      -lang "$LANGUAGE" -seed "$seed" -out "$src" ) > "$manifest"

  entry="$(jq -r .entry_module "$manifest")"
  appvar="$(jq -r .app_var "$manifest")"
  unit="$(jq -r .unit "$manifest")"
  LANGUAGE="$(jq -r '.language // "python"' "$manifest")"
  target="$entry:$appvar"
  log "unit $unit, serving $target"

  # ---------------------------------------------------------- A: as written
  log "running the program as written, against a real store and the SDK's emulations"
  rm -rf "$case_dir/local-state"
  reset_local_table
  if [ "$LANGUAGE" = "node" ]; then
    # The program as written imports the real SDK, so it has to be installed --
    # from the working tree, not a registry, so this tests what is in the repo.
    ( cd "$REPO_ROOT/sdk/node" && npm install --silent --no-audit --no-fund >/dev/null \
        && npm run build >/dev/null )
    ( cd "$src" && npm install --silent --no-audit --no-fund \
        express @aws-sdk/client-dynamodb @aws-sdk/client-s3 "$REPO_ROOT/sdk/node" >/dev/null )
  fi
  # AWS_ENDPOINT_URL is the AWS SDKs' own standard variable, honoured by both
  # boto3 and the JavaScript v3 clients, so the program as written needs no
  # cloudcc-specific configuration to reach the emulator.
  AWS_ENDPOINT_URL="$CLOUDCC_EMULATOR_ENDPOINT" \
  AWS_REGION="$AWS_REGION" \
  AWS_DEFAULT_REGION="$AWS_REGION" \
  AWS_ACCESS_KEY_ID="${AWS_ACCESS_KEY_ID:-cloudcc-local}" \
  AWS_SECRET_ACCESS_KEY="${AWS_SECRET_ACCESS_KEY:-cloudcc-local}" \
  CLOUDCC_LOCAL_STATE_DIR="$case_dir/local-state" \
    serve "$src" "$target" "$PORT_A" "uncompiled" \
      --with fastapi --with uvicorn --with boto3 \
      --with-editable "$REPO_ROOT/sdk/python"
  replay "$PORT_A" "$manifest" "$case_dir/uncompiled.txt"
  stop_app
  pass "uncompiled run recorded"

  # ------------------------------------------------------------ B: compiled
  log "compiling"
  "$WORK/cloudcc" "$src" -o "$out" >/dev/null
  # `-o` names the shared root; the artefacts are under the app's own folder.
  app_out_dir="$(app_out "$out" "$src")"

  log "deploying to the emulator"
  CURRENT_OUT="$out"
  "$WORK/cloudcc" deploy "$src" -o "$out" --stack local >/dev/null

  # The compiled unit reads its bindings from the stack, exactly as it would
  # in a real deployment.
  bindings="$(cd "$app_out_dir" && PULUMI_BACKEND_URL="file://$app_out_dir/.pulumi-state" \
    PULUMI_CONFIG_PASSPHRASE=cloudcc-emulator \
    pulumi stack output --json --stack local \
    | jq -r 'to_entries[] | select(.key | startswith("CLOUDCC_")) | "\(.key)=\(.value)"')"

  log "running the compiled program against the emulator"
  (
    set -a
    eval "$bindings"
    set +a
    export CLOUDCC_AWS_ENDPOINT_URL="$CLOUDCC_EMULATOR_ENDPOINT"
    serve "$app_out_dir/$unit" "$target" "$PORT_B" "compiled" \
      --with fastapi --with uvicorn --with boto3
    replay "$PORT_B" "$manifest" "$case_dir/compiled.txt"
    stop_app
  )
  pass "compiled run recorded"

  log "tearing down"
  "$WORK/cloudcc" deploy "$src" -o "$out" --stack local --destroy >/dev/null
  CURRENT_OUT=""

  # ------------------------------------------------------------- compare
  if diff -u "$case_dir/uncompiled.txt" "$case_dir/compiled.txt" > "$case_dir/diff.txt"; then
    pass "seed $seed: compiled behaviour is identical to uncompiled"
  else
    failures=$((failures + 1))
    printf '\033[1;31mFAIL\033[0m seed %s: compiling changed what the program does\n' "$seed" >&2
    echo "--- uncompiled (-) vs compiled (+) ---" >&2
    cat "$case_dir/diff.txt" >&2
    echo "--- the program ---" >&2
    find "$src" -name '*.py' -print -exec cat {} \; >&2
  fi
done

if [ "$failures" -gt 0 ]; then
  fail "$failures of ${#SEEDS[@]} programs behaved differently once compiled"
fi
log "every program behaved identically before and after compiling"
