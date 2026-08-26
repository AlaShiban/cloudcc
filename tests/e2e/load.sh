#!/usr/bin/env bash
# Load test: throughput, connectedness, and the before/after of compiling.
#
# For every example that can be deployed, this runs the same derived plan twice:
#
#   before  the example as written, one process, talking to real stores
#   after   the compiled application, deployed, with its units separated
#
# and reports what each sustained. The comparison is the point: compiling moves
# an application from one process to several behind a gateway, and this is the
# number that says what that cost.
#
# The second question is the one a load test does not usually ask. A compiled
# application is a graph, and the failure this project is organised against is
# an edge that looks right in the plan and is dead at runtime -- a store nothing
# wrote to, a topic whose subscriber never woke, a unit nobody invoked. Each of
# those is a green deploy and a broken application, and none of them shows up as
# an error. After the load has run, every runtime edge in the IR is checked for
# evidence that it carried something.
#
# The plan is derived from the compiler's own IR rather than written here, so an
# example that grows a route grows this test with it. The only thing supplied by
# hand is request bodies, which come from the scenario files the differential
# suite already uses -- no amount of reading a graph says an order needs items.
#
# Usage:
#   ./tests/e2e/load.sh [example ...]        # default: every deployable example
#   CLOUDCC_LOAD_SCALE=0.3 ./tests/e2e/load.sh   # shorter, for CI
set -euo pipefail

source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

SCENARIOS="$REPO_ROOT/tests/e2e/scenarios"
SCALE="${CLOUDCC_LOAD_SCALE:-1}"
PORT_A=8121
PORT_B=8122
KEEP="${CLOUDCC_E2E_KEEP:-0}"
REPORT_DIR="${CLOUDCC_LOAD_REPORTS:-$REPO_ROOT/compiled}"

WORK="$(mktemp -d "${TMPDIR:-/tmp}/cloudcc-load-XXXXXX")"
APP_PID=""
CURRENT_OUT=""
CURRENT_BACKEND=""

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
  # The backend URL travels with the directory. Without it `pulumi destroy`
  # looks in the default backend, finds no such stack, and leaves a whole
  # deployment behind -- which the next run then trips over as a pile of
  # "already exists" errors that say nothing about the actual failure.
  if [ -n "$CURRENT_OUT" ] && [ -d "$CURRENT_OUT" ]; then
    ( cd "$CURRENT_OUT" \
        && PULUMI_BACKEND_URL="$CURRENT_BACKEND" PULUMI_CONFIG_PASSPHRASE=cloudcc-emulator \
        pulumi destroy -y --stack ministack >/dev/null 2>&1 ) || true
  fi
  [ "$KEEP" = "0" ] && rm -rf "$WORK" || log "workdir kept at $WORK"
  exit $status
}
trap cleanup EXIT

require_endpoint

EXAMPLES=("$@")
if [ ${#EXAMPLES[@]} -eq 0 ]; then
  # Driven by the scenario directory, like the differential suite: an example
  # with no scenario is reported rather than quietly skipped.
  EXAMPLES=()
  for f in "$SCENARIOS"/*.json; do
    EXAMPLES+=("$(basename "$f" .json)")
  done
fi

log "building cloudcc and loadgen"
( cd "$REPO_ROOT" && go build -o "$WORK/cloudcc" ./cmd/cloudcc \
  && go build -o "$WORK/loadgen" ./internal/loadtest/cmd/loadgen )

# ------------------------------------------------------------- connectedness
#
# The checklist says which edges must have carried something; this is how to
# look. Counts come from the emulator, keyed by the exact edge string the plan
# used, and are handed back for the report to judge.

# observe <plan.json> <out-dir> -> writes observed.json
observe() {
  local plan="$1" out="$2" observations="{}"

  local edge kind target fallback
  while IFS=$'\t' read -r edge kind target fallback; do
    [ -n "$edge" ] || continue
    local count=0
    case "$kind" in
      http)
        # The gateway answered: taken from the run itself rather than from AWS,
        # since a locally-served unit has no gateway in front of it.
        count=1
        ;;
      store)
        local table
        table="$(stack_output "$out" "CLOUDCC_KV_$(env_slug "$target")_TABLE")"
        if [ -n "$table" ]; then
          count="$(aws_local dynamodb scan --table-name "$table" --select COUNT \
                   --output json 2>/dev/null | jq -r '.Count // 0')"
        fi
        ;;
      bucket)
        local bucket
        bucket="$(stack_output "$out" "CLOUDCC_FS_$(env_slug "$target")_BUCKET")"
        if [ -n "$bucket" ]; then
          count="$(aws_local s3api list-objects-v2 --bucket "$bucket" --output json 2>/dev/null \
                   | jq -r '(.Contents // []) | length')"
        fi
        ;;
      orm)
        # ANALYZE first: reltuples is an estimate maintained by the planner and
        # reads as zero on a table that has never been analysed, which on a
        # database this small is every table. It costs milliseconds here and
        # makes the number the actual row count.
        count="$(docker exec "$CLOUDCC_PG_CONTAINER" psql -U ccadmin -d petsdb -tAc \
          "ANALYZE; SELECT COALESCE(SUM(c.reltuples)::bigint, 0) FROM pg_class c
             JOIN pg_namespace n ON n.oid = c.relnamespace
            WHERE n.nspname = 'public' AND c.relkind = 'r';" 2>/dev/null \
          | tail -1 | tr -dc '0-9')"
        # tail -1 because psql echoes a command tag per statement: the output is
        # "ANALYZE" and then the count, and feeding both to jq --argjson is a
        # parse error a long way from the cause.
        ;;
      cache)
        count="$(docker exec "$CLOUDCC_REDIS_CONTAINER" redis-cli DBSIZE 2>/dev/null | tr -dc '0-9')"
        ;;
      secret)
        # Settled by the report rather than here: nothing observable is left
        # behind by reading one.
        count=0
        ;;
      invocations)
        count="$(unit_invocations "$target")"
        ;;
    esac
    observations="$(printf '%s' "$observations" \
      | jq --arg edge "$edge" --argjson n "${count:-0}" '. + {($edge): $n}')"

    # A companion count for store edges: how many times the unit at the edge's
    # source ran. An empty table plus a unit that ran is what a read-only path
    # looks like, and the report needs both numbers to say so rather than
    # calling the edge dead.
    if [ -n "$fallback" ] && [ "$fallback" != "null" ]; then
      local unit_streams
      unit_streams="$(unit_invocations "$fallback")"
      observations="$(printf '%s' "$observations" \
        | jq --arg edge "$edge#unit" --argjson n "${unit_streams:-0}" '. + {($edge): $n}')"
    fi
  done < <(jq -r '.expect[] | [.edge, .kind, .target, (.fallback // "")] | @tsv' "$plan")

  printf '%s' "$observations" > "$WORK/observed.json"
}

# require_emulator_still_up explains the one failure this harness provokes that
# has nothing to do with the code under test.
#
# Load is the heaviest thing anyone asks of the emulator: an application with
# six units has the emulator plus six Lambda containers resident at once, and on
# a Docker VM sized for ordinary use that can exhaust it. The container is then
# OOM-killed and every command after it fails with something unhelpful --
# "connection refused", or an empty JSON document that reads as a dead edge.
#
# A wrong answer here is worse than no answer: reporting an edge as dead when
# the emulator has died would send someone looking for a bug in their program.
require_emulator_still_up() {
  if curl -sf -m 5 -o /dev/null "$MINISTACK_ENDPOINT" 2>/dev/null; then
    return 0
  fi
  fail "the emulator at $MINISTACK_ENDPOINT stopped answering during the run.
  A load test is the heaviest thing this emulator is asked to do -- the
  application's units run as containers beside it -- and on a small Docker VM
  it can be killed for memory. Nothing here is evidence about $EXAMPLE either
  way; the run is void rather than failed.

  Check with:   docker ps -a --filter ancestor=ministackorg/ministack
  A status of 'Exited (137)' is the out-of-memory kill.
  Give the VM more memory (colima stop && colima start --memory 6), or run
  with a smaller CLOUDCC_LOAD_SCALE."
}

# unit_invocations counts the log streams a unit left behind.
#
# It is the only signal the emulator offers for "this function ran" that does
# not require the unit to have written to a store -- and a unit whose whole job
# is to answer a call may never write to one.
unit_invocations() {
  local app fn
  app="$(app_name "$REPO_ROOT/examples/$EXAMPLE")"
  fn="/aws/lambda/${app}-${1}"
  aws_local logs describe-log-streams --log-group-name "$fn" --output json 2>/dev/null \
    | jq -r '(.logStreams // []) | length'
}

# env_slug mirrors sanitize.EnvVar: the environment spelling of a capability id.
env_slug() {
  printf '%s' "$1" | LC_ALL=C tr 'a-z' 'A-Z' | LC_ALL=C sed 's/[^A-Z0-9]/_/g'
}

stack_output() {
  ( cd "$1" \
      && PULUMI_BACKEND_URL="$CURRENT_BACKEND" PULUMI_CONFIG_PASSPHRASE=cloudcc-emulator \
      pulumi stack output --json --stack ministack 2>/dev/null ) \
    | jq -r --arg key "$2" '.[$key] // ""'
}

# ---------------------------------------------------------------- serving

# serve <dir> <language> <target> <port> <label> <env> <with-sdk>
#
# with-sdk is "yes" only for the uncompiled half. The compiled half must run
# without the SDK on its path at all: its hint calls were rewritten away, the
# package is not in a deployment bundle, and quietly making it importable here
# would hide the one failure that matters -- a rewrite that missed something.
serve() {
  local dir="$1" language="$2" target="$3" port="$4" label="$5" env_pairs="$6" with_sdk="${7:-no}"
  local sdk=()
  [ "$with_sdk" = "yes" ] && sdk=(--with-editable "$REPO_ROOT/sdk/python")

  if lsof -ti:"$port" >/dev/null 2>&1; then
    fail "port $port is already in use; a server from an earlier run is still up"
  fi

  if [ "$language" = "node" ]; then
    local entry="${target%%:*}" binding="${target##*:}"
    # Nothing is written into the directory being served. The uncompiled half
    # is the user's own source tree, and a harness that drops a launcher into it
    # is modifying the input -- which this compiler does not do and its tests
    # should not either. An absolute specifier resolves the module's own
    # node_modules from where the module is, so no file is needed.
    ( cd "$dir" && exec env $env_pairs node --input-type=module -e "
      const m = await import(\"file://$dir/$entry\");
      const app = m.$binding ?? m.default;
      app.listen($port, \"127.0.0.1\", () => console.log(\"listening\"));
    " ) >"$WORK/$label.log" 2>&1 &
  else
    ( cd "$dir" && exec env $env_pairs PYTHONPATH="$dir" \
        uv run --quiet $(py_run_deps "$dir") \
          ${sdk[@]+"${sdk[@]}"} \
          python -m uvicorn "$target" --host 127.0.0.1 --port "$port" \
            --log-level warning --no-access-log ) >"$WORK/$label.log" 2>&1 &
  fi
  APP_PID=$!

  wait_for_http "http://127.0.0.1:$port/health" 60 \
    || { sed 's/^/    /' "$WORK/$label.log" | tail -20; fail "the $label application did not start"; }
}

# ------------------------------------------------------------------- main

failures=0
skipped=0
compared=0

for EXAMPLE in "${EXAMPLES[@]}"; do
  scenario="$SCENARIOS/$EXAMPLE.json"
  src="$REPO_ROOT/examples/$EXAMPLE"

  if [ ! -f "$scenario" ]; then
    warn "examples/$EXAMPLE has no scenario; it is not load tested"
    failures=$((failures + 1))
    continue
  fi
  # An example that cannot be deployed cannot be load tested either, and the
  # reason is already written down once.
  skip="$(jq -r '.skip // empty' "$scenario")"
  has_load="$(jq -r 'if .load then "yes" else "no" end' "$scenario")"
  if [ -n "$skip" ] && [ "$has_load" != "yes" ]; then
    warn "SKIP $EXAMPLE: $skip"
    skipped=$((skipped + 1))
    continue
  fi

  log "================ $EXAMPLE ================"
  # Checked per example, not only at the start: a suite that runs several in
  # sequence is exactly where the emulator runs out of memory, and the first
  # symptom is a fixture that will not create -- which reads as a broken
  # harness rather than a dead emulator.
  require_emulator_still_up
  case_dir="$WORK/$EXAMPLE"
  mkdir -p "$case_dir"

  "$WORK/cloudcc" --dump-ir "$src" > "$case_dir/ir.json" 2>/dev/null \
    || fail "$EXAMPLE: could not dump the IR"
  "$WORK/loadgen" -plan -ir "$case_dir/ir.json" -seed "$scenario" -app "$EXAMPLE" \
    > "$case_dir/plan.json" || fail "$EXAMPLE: could not derive a plan"

  # An application whose writes fan out to Lambda subscribers is bounded by the
  # emulator rather than by itself: it starts a container per delivery and does
  # not reap them, so a few hundred published messages exhaust it whatever the
  # VM is sized at -- 2 GB, 6 GB and 10 GB all died at the same point. The
  # symptom is an emulator killed for memory partway through, and then a
  # subscription reported as dead because nothing was delivered after it went.
  #
  # So a scenario may name the scale its own fan-out can survive. It is not a
  # statement about the application; it is the emulator's ceiling, written
  # where the next person will see it.
  scale="$SCALE"
  scenario_scale="$(jq -r '.load.scale // empty' "$scenario")"
  if [ -n "$scenario_scale" ]; then
    scale="$(awk -v a="$SCALE" -v b="$scenario_scale" 'BEGIN{print (a<b)?a:b}')"
    log "scale $scale (this example caps it at $scenario_scale)"
  fi

  unit="$(jq -r '.unit' "$case_dir/plan.json")"
  language="$(unit_language "$WORK/cloudcc" "$src" "$unit")"
  target="$(unit_target "$WORK/cloudcc" "$src" "$unit")" \
    || fail "$EXAMPLE: could not work out how to serve unit $unit"
  log "unit $unit ($language), serving $target"
  log "plan: $(jq -r '[.steps[] | "\(.verb) \(.path)"] | join(", ")' "$case_dir/plan.json")"

  # ---------------------------------------------------------- before
  #
  # The example as written, against real stores in the emulator under the local
  # names the program itself uses.
  #
  # From a copy, never from examples/ itself. Serving a Node unit means
  # installing its dependencies, and installing them into the repository would
  # leave a node_modules and a lockfile behind in the user's source tree --
  # which is the one thing this compiler promises not to do, and its tests
  # should hold to the same rule.
  work_src="$case_dir/src"
  cp -R "$src" "$work_src"

  # From either place: the differential suite declares them at the top level,
  # and an example it skips declares them under "load" instead.
  ensure_engines "$scenario"
  reset_engines "$scenario"

  for table in $(jq -r '[(.tables // []), (.load.tables // [])] | flatten | .[]' "$scenario"); do
    reset_local_table "$table"
  done
  for bucket in $(jq -r '[(.buckets // []), (.load.buckets // [])] | flatten | .[]' "$scenario"); do
    ensure_local_bucket "$bucket"
  done

  if [ "$language" = "node" ]; then
    # From the working tree rather than a registry, so this exercises the SDK
    # in the repository.
    ( cd "$REPO_ROOT/sdk/node" && npm install --silent --no-audit --no-fund >/dev/null \
        && npm run build >/dev/null ) || fail "$EXAMPLE: could not build the Node SDK"
    ( cd "$work_src" && npm install --silent --no-audit --no-fund >/dev/null \
        && npm install --silent --no-audit --no-fund "$REPO_ROOT/sdk/node" >/dev/null ) \
      || fail "$EXAMPLE: could not install the example's Node dependencies"
  fi

  log "load: the example as written"
  serve "$work_src" "$language" "$target" "$PORT_A" "$EXAMPLE-before" "$(local_aws_env | tr ';' ' ')" yes
  "$WORK/loadgen" -ir "$case_dir/ir.json" -seed "$scenario" -app "$EXAMPLE" \
    -url "http://127.0.0.1:$PORT_A" -mode uncompiled -scale "$scale" \
    -out "$case_dir/before.json" || fail "$EXAMPLE: the uncompiled run failed"
  stop_app

  # The scratch stores the uncompiled half used are torn down before the
  # compiled half provisions its own.
  #
  # They can collide: a program whose local table is "nomnom-orders" and an
  # application called "nomnom" with a store called "orders" arrive at the same
  # physical name, and `pulumi up` then fails with ResourceInUseException --
  # which reads as a compiler bug and is really a fixture left lying about.
  for table in $(jq -r '[(.tables // []), (.load.tables // [])] | flatten | .[]' "$scenario"); do
    aws_local dynamodb delete-table --table-name "$table" >/dev/null 2>&1 || true
  done

  # ---------------------------------------------------------- after
  out="$case_dir/out"
  "$WORK/cloudcc" "$src" -o "$out" >/dev/null 2>&1 || fail "$EXAMPLE: compile failed"
  app_out_dir="$(app_out "$out" "$src")"
  CURRENT_OUT="$app_out_dir"
  CURRENT_BACKEND="file://$case_dir/pulumi-state"

  ( cd "$app_out_dir" && npm install --silent --no-audit --no-fund ) \
    || fail "$EXAMPLE: npm install failed"

  if TARGET="$(emulator_python_target)"; then
    export CLOUDCC_PYTHON_PLATFORM="${TARGET%% *}" CLOUDCC_PYTHON_VERSION="${TARGET##* }"
  fi
  ( cd "$app_out_dir" && ./bin/package.sh >/dev/null ) || fail "$EXAMPLE: packaging failed"

  (
    cd "$app_out_dir"
    export PULUMI_BACKEND_URL="file://$case_dir/pulumi-state"
    export PULUMI_CONFIG_PASSPHRASE=cloudcc-emulator
    mkdir -p "$case_dir/pulumi-state"
    # Not suppressed: an init that fails for a reason other than "it already
    # exists" is otherwise invisible, and the select after it then reports a
    # missing stack -- which sends the reader looking in the wrong place.
    if ! pulumi stack select ministack >/dev/null 2>&1; then
      pulumi stack init ministack --non-interactive || exit 1
    fi
    PULUMI_CONFIG_PASSPHRASE=cloudcc-emulator pulumi_configure_emulator ministack
    # Captured rather than discarded. Pulumi reports a failed update on
    # stdout along with the resource list, so suppressing the noise suppresses
    # the reason too -- and "pulumi up failed" on its own is not a diagnosis.
    pulumi up -y --stack ministack --non-interactive > "$case_dir/pulumi-up.log" 2>&1
  ) || {
    sed 's/^/    /' "$case_dir/pulumi-up.log" | tail -30
    fail "$EXAMPLE: pulumi up failed"
  }

  bindings="$( ( cd "$app_out_dir" \
      && PULUMI_BACKEND_URL="$CURRENT_BACKEND" PULUMI_CONFIG_PASSPHRASE=cloudcc-emulator \
      pulumi stack output --json --stack ministack ) \
    | jq -r 'to_entries[] | select(.key | startswith("CLOUDCC_")) | "\(.key)=\(.value)"' | tr '\n' ' ')"
  bindings="$(cache_endpoints_local "$bindings")"
  seed_secrets "$( ( cd "$app_out_dir" \
      && PULUMI_BACKEND_URL="$CURRENT_BACKEND" PULUMI_CONFIG_PASSPHRASE=cloudcc-emulator \
      pulumi stack output --json --stack ministack ) )"

  # Again, before the compiled half. The table and the bucket differ between
  # the two runs -- one is the local name, the other is provisioned -- but the
  # engines are literally the same containers, because the emulator cannot run
  # them. Rows the first half wrote would otherwise be counted twice by the
  # second, and the two runs would differ for a reason that has nothing to do
  # with compiling.
  reset_engines "$scenario"
  require_emulator_still_up
  log "load: the compiled application"
  serve "$app_out_dir/$unit" "$language" "$target" "$PORT_B" "$EXAMPLE-after" \
    "$bindings CLOUDCC_AWS_ENDPOINT_URL=$MINISTACK_ENDPOINT"
  "$WORK/loadgen" -ir "$case_dir/ir.json" -seed "$scenario" -app "$EXAMPLE" \
    -url "http://127.0.0.1:$PORT_B" -mode compiled -scale "$scale" \
    -out "$case_dir/after.json" || fail "$EXAMPLE: the compiled run failed"
  stop_app

  # ------------------------------------------------- connectedness
  #
  # After the load, not during it: an edge is being checked for evidence that
  # it carried something, and the asynchronous half of an application is still
  # catching up when the last response goes out.
  # Wait for the evidence rather than for a fixed number of seconds.
  #
  # A subscriber is invoked some time after the publish that woke it, and how
  # long depends on what else the emulator is doing. A fixed sleep is a guess
  # that is either wasteful or wrong, and when it is wrong the harness reports
  # a dead edge for an application that works -- which is the worst answer it
  # can give, because it is the one people act on.
  #
  # So: poll until every asynchronous edge has evidence, and stop early when
  # they all do. An edge that really is dead costs the full timeout once.
  log "waiting for the asynchronous half to settle"
  async_units="$(jq -r '.expect[] | select(.kind == "invocations") | .target' "$case_dir/plan.json" | sort -u)"
  waited=0
  while [ "$waited" -lt 90 ]; do
    pending=0
    for unit in $async_units; do
      [ "$(unit_invocations "$unit")" -gt 0 ] 2>/dev/null || pending=$((pending + 1))
    done
    [ "$pending" -eq 0 ] && break
    sleep 3
    waited=$((waited + 3))
  done
  if [ "${pending:-0}" -gt 0 ]; then
    log "$pending asynchronous edge(s) still had no evidence after ${waited}s"
  else
    log "every asynchronous edge had evidence after ${waited}s"
  fi

  log "checking every edge"
  require_emulator_still_up
  observe "$case_dir/plan.json" "$app_out_dir"

  "$WORK/loadgen" -attach "$case_dir/after.json" -observed "$WORK/observed.json" \
    || fail "$EXAMPLE: could not attach the connectedness checklist"

  ( cd "$app_out_dir" \
      && PULUMI_BACKEND_URL="$CURRENT_BACKEND" PULUMI_CONFIG_PASSPHRASE=cloudcc-emulator \
      pulumi destroy -y --stack ministack >/dev/null 2>&1 ) || true
  CURRENT_OUT=""

  # ------------------------------------------------------- report
  echo
  # Said once per example rather than buried in a README: the compiled side is
  # talking to an emulator, whose Lambda invoke is far slower than the real
  # one. The ratio is a measurement of this machine and this emulator, and is
  # worth watching for change rather than reading as a production figure.
  log "ratios below are against $MINISTACK_ENDPOINT, not real AWS"
  if "$WORK/loadgen" -compare "$case_dir/before.json" "$case_dir/after.json"; then
    pass "$EXAMPLE: every edge carried traffic, and the benchmark is above"
    compared=$((compared + 1))
  else
    warn "$EXAMPLE: the compiled application has edges nothing reached"
    failures=$((failures + 1))
  fi

  mkdir -p "$REPORT_DIR/$EXAMPLE"
  cp "$case_dir/before.json" "$REPORT_DIR/$EXAMPLE/loadtest-uncompiled.json"
  cp "$case_dir/after.json" "$REPORT_DIR/$EXAMPLE/loadtest-compiled.json"
  log "reports written to $REPORT_DIR/$EXAMPLE/"
  echo
done

log "$compared example(s) load tested, $skipped skipped with a reason, $failures failure(s)"
[ "$failures" -eq 0 ] || fail "load testing found $failures problem(s)"
pass "every load-tested example sustained traffic on every edge the compiler drew"
