#!/usr/bin/env bash
# Every example, run twice: as written, and after compiling.
#
#   A. uncompiled -- the example exactly as it appears in the repository,
#                    talking to real stores in the emulator
#   B. compiled   -- the same source after cloudcc rewrote it, deployed to the
#                    emulator, talking to the resources cloudcc provisioned
#
# The same requests go to both and every response must match, byte for byte.
#
# This is the guarantee the compiler actually owes, applied to the code people
# will read first. tests/e2e/differential.sh makes the same comparison for
# *generated* programs, which is better at covering shapes; this one is better
# at covering the thing a newcomer copies. Both matter, and neither substitutes
# for the other: the generator would never have written examples/mixed, and no
# handwritten example covers twenty spellings of an import.
#
# Every example in examples/ is accounted for. The two that cannot deploy say
# why, out loud, rather than being quietly left out of a loop -- a suite that
# silently covers four of six looks exactly like a suite that covers six.
#
# Usage:
#   ./tests/e2e/examples.sh [example ...]      # default: all of them
set -euo pipefail

source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

SCENARIOS="$REPO_ROOT/tests/e2e/scenarios"
PORT_A=8111
PORT_B=8112

EXAMPLES=("$@")
if [ ${#EXAMPLES[@]} -eq 0 ]; then
  # Driven by the scenario directory rather than by a list in this file, so a
  # new example is covered by adding one file -- and an example with no
  # scenario at all is reported below rather than skipped.
  EXAMPLES=()
  for f in "$SCENARIOS"/*.json; do
    EXAMPLES+=("$(basename "$f" .json)")
  done
fi

WORK="$(mktemp -d "${TMPDIR:-/tmp}/cloudcc-examples-XXXXXX")"
KEEP="${CLOUDCC_E2E_KEEP:-0}"
APP_PID=""
CURRENT_OUT=""
CURRENT_SRC=""

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
    "$WORK/cloudcc" deploy "$CURRENT_SRC" -o "$CURRENT_OUT" \
      --stack ministack --destroy >/dev/null 2>&1 || true
  fi
  [ "$KEEP" = "0" ] && rm -rf "$WORK" || log "workdir kept at $WORK"
  exit $status
}
trap cleanup EXIT

require_endpoint
log "emulator: $MINISTACK_ENDPOINT"
log "examples: ${EXAMPLES[*]}"

log "building cloudcc"
( cd "$REPO_ROOT" && go build -o "$WORK/cloudcc" ./cmd/cloudcc )

# serve starts a program on a port and waits for /health.
#
# uvicorn for Python; for Node a launcher this script writes, which plays
# exactly uvicorn's role and is no more part of the application than uvicorn is.
serve() {
  local dir="$1" language="$2" target="$3" port="$4" label="$5" env_pairs="$6"
  local entry="${target%%:*}" appvar="${target##*:}"

  if [ "$language" = "node" ]; then
    cat > "$dir/cloudcc_e2e_serve.mjs" <<SERVE
const m = await import("./$entry");
const app = m.$appvar ?? m.default;
if (!app) {
  console.error("the entry exports neither $appvar nor default");
  process.exit(1);
}
app.listen($port, "127.0.0.1");
SERVE
    ( cd "$dir" && exec env $env_pairs node cloudcc_e2e_serve.mjs ) &
  else
    ( cd "$dir" && exec env $env_pairs PYTHONPATH="$dir" \
        uv run --quiet $(py_run_deps "$dir") \
          --with-editable "$REPO_ROOT/sdk/python" \
          python -m uvicorn "$target" --host 127.0.0.1 --port "$port" --log-level error ) &
  fi
  APP_PID=$!
  wait_for_http "http://127.0.0.1:$port/health" 60 \
    || fail "the $label program did not start on port $port"
}

# replay issues every request in a scenario and writes one normalised line per
# step, so the two runs can be compared with a plain diff.
replay() {
  local port="$1" scenario="$2" out="$3"
  : > "$out"

  local count
  count="$(jq '.requests | length' "$scenario")"
  for ((i = 0; i < count; i++)); do
    local method path body status tmp raw
    method="$(jq -r ".requests[$i].method" "$scenario")"
    path="$(jq -r ".requests[$i].path" "$scenario")"
    body="$(jq -c ".requests[$i].body // empty" "$scenario")"
    tmp="$WORK/response.$$"

    if [ -n "$body" ]; then
      status="$(curl -s -o "$tmp" -w '%{http_code}' -X "$method" \
        -H 'content-type: application/json' -d "$body" "http://127.0.0.1:$port$path")"
    else
      status="$(curl -s -o "$tmp" -w '%{http_code}' -X "$method" "http://127.0.0.1:$port$path")"
    fi

    # Sort object keys, so an incidental ordering difference is not mistaken
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
skipped=0
checked=0

for example in "${EXAMPLES[@]}"; do
  scenario="$SCENARIOS/$example.json"
  src="$REPO_ROOT/examples/$example"

  echo
  log "================ $example ================"

  if [ ! -f "$scenario" ]; then
    warn "examples/$example has no scenario in tests/e2e/scenarios; it is not covered"
    failures=$((failures + 1))
    continue
  fi
  if [ ! -d "$src" ]; then
    warn "tests/e2e/scenarios/$example.json describes an example that does not exist"
    failures=$((failures + 1))
    continue
  fi

  skip="$(jq -r '.skip // empty' "$scenario")"
  if [ -n "$skip" ]; then
    skipped=$((skipped + 1))
    printf '\033[1;33m skip\033[0m %s: %s\n' "$example" "$skip"
    continue
  fi

  unit="$(jq -r '.unit' "$scenario")"
  case_dir="$WORK/$example"
  out="$case_dir/out"
  mkdir -p "$case_dir"

  language="$(unit_language "$WORK/cloudcc" "$src" "$unit")"
  target="$(unit_target "$WORK/cloudcc" "$src" "$unit")" \
    || fail "$example: unit $unit exposes no application, so there is nothing to compare"
  log "unit $unit ($language), serving $target"

  # ------------------------------------------------------------- A: as written
  #
  # The example is copied rather than served in place: the uncompiled run
  # installs dependencies and writes a launcher, and the repository is not a
  # scratch directory.
  log "running the example as written"
  work_src="$case_dir/src"
  rm -rf "$work_src"
  cp -R "$src" "$work_src"

  # A relational store and a cache are real engines rather than emulator
  # resources: the emulator provisions RDS and ElastiCache but runs nothing
  # behind them. Both halves talk to the same containers, which is what makes
  # the comparison a comparison.
  ensure_engines "$scenario"
  reset_engines "$scenario"

  for table in $(jq -r '.tables // [] | .[]' "$scenario"); do
    reset_local_table "$table"
  done
  for bucket in $(jq -r '.buckets // [] | .[]' "$scenario"); do
    ensure_local_bucket "$bucket"
  done

  if [ "$language" = "node" ]; then
    # From the working tree, not a registry, so this tests what is in the repo.
    ( cd "$REPO_ROOT/sdk/node" && npm install --silent --no-audit --no-fund >/dev/null \
        && npm run build >/dev/null )
    ( cd "$work_src" && npm install --silent --no-audit --no-fund >/dev/null \
        && npm install --silent --no-audit --no-fund "$REPO_ROOT/sdk/node" >/dev/null )
  fi

  env_pairs="$(local_aws_env | tr ';' ' ')"
  serve "$work_src" "$language" "$target" "$PORT_A" "uncompiled" "$env_pairs"
  replay "$PORT_A" "$scenario" "$case_dir/uncompiled.txt"
  stop_app
  pass "$example: uncompiled run recorded"

  # -------------------------------------------------------------- B: compiled
  log "compiling and deploying"
  CURRENT_SRC="$src"
  "$WORK/cloudcc" "$src" -o "$out" >/dev/null
  # `-o` names the shared root; the artefacts are under the app's own folder.
  app_out_dir="$(app_out "$out" "$src")"
  CURRENT_OUT="$out"

  # Every application gets both diagrams, and the architecture one is only
  # worth having if it matches what was actually deployed -- so check it names
  # the resources the stack is about to create rather than merely existing.
  for diagram in topology.mmd topology.dot architecture.mmd architecture.dot architecture.py; do
    [ -s "$app_out_dir/$diagram" ] || fail "$example: no $diagram was written"
  done
  # The icon diagram is a program, so "it was written" is a weak claim: check
  # it at least parses. Whether it renders is checked in the offline job, where
  # the package is installed.
  if command -v python3 >/dev/null 2>&1; then
    python3 -c "import sys; compile(open(sys.argv[1]).read(), sys.argv[1], 'exec')" \
      "$app_out_dir/architecture.py" \
      || fail "$example: architecture.py is not valid Python"
  fi
  pass "$example: every diagram written, and architecture.py parses"

  "$WORK/cloudcc" deploy "$src" -o "$out" --stack ministack >/dev/null

  bindings="$(cd "$app_out_dir" && PULUMI_BACKEND_URL="file://$app_out_dir/.pulumi-state" \
    PULUMI_CONFIG_PASSPHRASE=cloudcc-emulator \
    pulumi stack output --json --stack ministack \
    | jq -r 'to_entries[] | select(.key | startswith("CLOUDCC_")) | "\(.key)=\(.value)"' \
    | tr '\n' ' ')"
  bindings="$(engine_bindings_local "$bindings")"

  # The compiled *sources*, for both languages.
  #
  # A Node unit's build/<unit> holds only the bundled index.mjs, which exports a
  # Lambda handler rather than the app. A Python unit's build/<unit> is the
  # unpacked deployment bundle, and its wheels are resolved for the deployment
  # target -- Linux, and a libc and architecture that are usually not this
  # machine's -- so a compiled extension in it cannot be imported here. That is
  # correct for what gets deployed and useless for running locally.
  #
  # What this suite compares is behaviour before and after the rewrite, so the
  # rewritten source is the thing to run, with the host's own dependencies.
  unit_dir="$app_out_dir/$unit"

  # Again, before the compiled half. The table and the bucket differ between
  # the two runs -- one is the local name, the other is provisioned -- but the
  # engines are literally the same containers, because the emulator cannot run
  # them. Rows the first half wrote would otherwise be counted twice by the
  # second, and the two runs would differ for a reason that has nothing to do
  # with compiling.
  reset_engines "$scenario"
  log "running the compiled copy against the emulator"
  serve "$unit_dir" "$language" "$target" "$PORT_B" "compiled" \
    "$bindings CLOUDCC_AWS_ENDPOINT_URL=$MINISTACK_ENDPOINT"
  replay "$PORT_B" "$scenario" "$case_dir/compiled.txt"
  stop_app
  pass "$example: compiled run recorded"

  # The architecture diagram claims to show every resource that will exist.
  # Pulumi knows exactly what it just created, so the two can be compared --
  # which is the only check that stops the picture drifting from the deployment
  # while still looking plausible.
  #
  # The two name things differently: a URN carries a Pulumi type
  # (aws:dynamodb/table:Table) and the diagram carries a node kind
  # (aws_dynamodb_petsByOwner). What is comparable is the service word in each,
  # plus the count -- and the count is what catches a resource that exists and
  # is not drawn.
  deployed_types="$(cd "$app_out_dir" && PULUMI_BACKEND_URL="file://$app_out_dir/.pulumi-state" \
    PULUMI_CONFIG_PASSPHRASE=cloudcc-emulator \
    pulumi stack export --stack ministack 2>/dev/null \
    | jq -r '.deployment.resources // [] | .[] | .urn' \
    | awk -F'::' '{print $3}' | grep '^aws:' | sort)"
  deployed_count="$(printf '%s\n' "$deployed_types" | grep -c . || true)"
  # A node line is an indented id that is not an edge. Matching on the shape
  # opener instead looks tidier and is wrong: a topic's shape opens with ">",
  # which an obvious-looking character class quietly misses -- and the check
  # then reports two resources missing from a diagram that has them all.
  drawn_count="$(grep -E '^    aws_' "$app_out_dir/architecture.mmd" \
    | grep -vcE '\-\->|\-\.\->' || true)"

  # A check that compared nothing passes for the wrong reason, which is worse
  # than no check: it reads as "the diagram is complete" when it means "the
  # export was empty".
  [ "$deployed_count" -gt 0 ] \
    || fail "$example: read no resources out of the deployed stack, so the diagram was not checked"

  # A few resources are named for the service that hosts them and drawn under
  # the thing they are. A VPC, its subnets and its route tables all live in the
  # ec2 API; nobody calls them ec2 resources, and the compiler does not either.
  missing=0
  for service in $(printf '%s\n' "$deployed_types" | cut -d: -f2 | cut -d/ -f1 | sort -u); do
    case "$service" in
      ec2) service=vpc ;;
    esac
    grep -q "aws_${service}" "$app_out_dir/architecture.mmd" \
      || { warn "$example: $service is deployed but appears nowhere in architecture.mmd"; missing=$((missing + 1)); }
  done
  if [ "$deployed_count" != "$drawn_count" ]; then
    warn "$example: the stack has $deployed_count resources and the diagram draws $drawn_count"
    missing=$((missing + 1))
  fi
  if [ "$missing" -eq 0 ]; then
    pass "$example: the architecture diagram matches the $deployed_count deployed resources"
  else
    failures=$((failures + 1))
  fi

  log "tearing down"
  "$WORK/cloudcc" deploy "$src" -o "$out" --stack ministack --destroy >/dev/null
  CURRENT_OUT=""

  # ----------------------------------------------------------------- compare
  checked=$((checked + 1))

  # Identical is not the same as working. A route that raises in both halves
  # produces the same 500 twice and sails through the diff below, which reads
  # as "the compiler preserved behaviour" when it means "both are broken".
  #
  # That is not hypothetical: an async ORM session that expires its attributes
  # on commit raised MissingGreenlet on every write, in both runs, and this
  # suite reported the example green. A scenario is a list of requests somebody
  # expected to succeed, so a server error is a failure of the example whether
  # or not the two halves agree on it.
  for half in uncompiled compiled; do
    if grep -qE ' -> 5[0-9][0-9] ' "$case_dir/$half.txt"; then
      failures=$((failures + 1))
      printf '\033[1;31mFAIL\033[0m %s: the %s run returned a server error\n' "$example" "$half"
      grep -E ' -> 5[0-9][0-9] ' "$case_dir/$half.txt"
    fi
  done

  if diff -u "$case_dir/uncompiled.txt" "$case_dir/compiled.txt" > "$case_dir/diff.txt"; then
    pass "$example: compiled behaviour is identical to uncompiled"
  else
    failures=$((failures + 1))
    printf '\033[1;31mFAIL\033[0m %s: the compiled program behaved differently\n' "$example"
    cat "$case_dir/diff.txt"
  fi
done

echo
log "$checked example(s) compared, $skipped skipped with a reason, $failures failure(s)"
[ "$failures" -eq 0 ] || exit 1
pass "every deployable example behaves identically before and after compiling"
