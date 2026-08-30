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
      --stack local --destroy >/dev/null 2>&1 || true
  fi
  [ "$KEEP" = "0" ] && rm -rf "$WORK" || log "workdir kept at $WORK"
  exit $status
}
trap cleanup EXIT

require_endpoint
log "emulator: $CLOUDCC_EMULATOR_ENDPOINT"
log "examples: ${EXAMPLES[*]}"

log "building cloudcc"
( cd "$REPO_ROOT" && go build -o "$WORK/cloudcc" ./cmd/cloudcc )

# serve starts a program on a port and waits for /health.
#
# uvicorn for Python; serve_node for Node and TypeScript, which plays exactly
# uvicorn's role and is no more part of the application than uvicorn is.
serve() {
  local dir="$1" language="$2" target="$3" port="$4" label="$5" env_pairs="$6"
  local logfile="${7:-}"
  local entry="${target%%:*}" appvar="${target##*:}"

  # Tracing is on for both halves. With CLOUDCC_TRACE unset the tracer returns
  # the client untouched, so this is the only thing that turns it on, and the
  # log is where the events land -- see trace_from_log.
  env_pairs="$env_pairs $(trace_env)"

  if [ "$language" = "node" ]; then
    serve_node "$dir" "$entry" "$appvar" "$port" "$logfile" "$env_pairs"
  elif [ -n "$logfile" ]; then
    ( cd "$dir" && exec env $env_pairs PYTHONPATH="$dir" \
        uv run --quiet $(py_run_deps "$dir") \
          --with-editable "$REPO_ROOT/sdk/python" \
          python -m uvicorn "$target" --host 127.0.0.1 --port "$port" --log-level error ) \
      >"$logfile" 2>&1 &
    APP_PID=$!
  else
    ( cd "$dir" && exec env $env_pairs PYTHONPATH="$dir" \
        uv run --quiet $(py_run_deps "$dir") \
          --with-editable "$REPO_ROOT/sdk/python" \
          python -m uvicorn "$target" --host 127.0.0.1 --port "$port" --log-level error ) &
    APP_PID=$!
  fi
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

  # Secrets too, and the same values the compiled half will read out of Secrets
  # Manager. A route that uses a secret is otherwise untestable here: the local
  # half would read an empty string and the two could never match.
  env_pairs="$(printf '%s;%s' "$(local_aws_env)" "$(secret_env "$WORK/cloudcc" "$src")" | tr ';' ' ')"
  serve "$work_src" "$language" "$target" "$PORT_A" "uncompiled" "$env_pairs" \
    "$case_dir/uncompiled.log"
  replay "$PORT_A" "$scenario" "$case_dir/uncompiled.txt"
  stop_app
  trace_from_log "$case_dir/uncompiled.log" "$case_dir/uncompiled.trace"
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

  # A config value with no default is required, and `pulumi up` refuses before
  # creating anything until somebody supplies it. That somebody is an operator,
  # so the harness plays one.
  seed_stack_config "$WORK/cloudcc" "$src" "$app_out_dir" local

  "$WORK/cloudcc" deploy "$src" -o "$out" --stack local >/dev/null

  bindings="$(cd "$app_out_dir" && PULUMI_BACKEND_URL="file://$app_out_dir/.pulumi-state" \
    PULUMI_CONFIG_PASSPHRASE=cloudcc-emulator \
    pulumi stack output --json --stack local \
    | jq -r 'to_entries[] | select(.key | startswith("CLOUDCC_")) | "\(.key)=\(.value)"' \
    | tr '\n' ' ')"
  bindings="$(engine_bindings_local "$bindings")"

  # The compiler provisions a secret and deliberately not its contents -- a
  # value in the generated project would be a value in the repository. Setting
  # it is an operator's job, done once, out of band.
  seed_secrets "$(cd "$app_out_dir" && PULUMI_BACKEND_URL="file://$app_out_dir/.pulumi-state" \
    PULUMI_CONFIG_PASSPHRASE=cloudcc-emulator pulumi stack output --json --stack local 2>/dev/null)"

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

  # A compiled TypeScript unit has to type-check.
  #
  # The compiled copy is source someone reads and diffs, and the injected
  # runtime is JavaScript -- so without declarations beside it, `tsc` reports
  # every shim import as an implicit any, and `persist` losing its type takes
  # every inference downstream of a store with it. Both are errors in code the
  # user did not write and cannot fix, and neither shows up anywhere else in
  # this suite: the unit runs perfectly well untyped.
  #
  # Only when the unit is TypeScript and carries a tsconfig. The dev
  # dependencies are installed first because packaging deliberately omits them
  # -- a deployed bundle has no use for @types/express -- and someone
  # type-checking the compiled copy is a developer, who has them. npx supplies
  # the compiler itself for the same reason.
  if [ -f "$unit_dir/tsconfig.json" ] && \
     [ -n "$(find "$unit_dir" -name node_modules -prune -o -name '*.ts' -print 2>/dev/null | head -1)" ]; then
    ( cd "$unit_dir" && npm install --silent --no-audit --no-fund >/dev/null 2>&1 ) || true
    if ( cd "$unit_dir" && npx --yes -p typescript@5 tsc --noEmit >"$WORK/tsc-$example.log" 2>&1 ); then
      pass "$example: the compiled TypeScript unit type-checks"
    else
      sed 's/^/    /' "$WORK/tsc-$example.log" | head -20
      fail "$example: the compiled unit does not type-check"
    fi
  fi

  # Again, before the compiled half. The table and the bucket differ between
  # the two runs -- one is the local name, the other is provisioned -- but the
  # engines are literally the same containers, because the emulator cannot run
  # them. Rows the first half wrote would otherwise be counted twice by the
  # second, and the two runs would differ for a reason that has nothing to do
  # with compiling.
  reset_engines "$scenario"
  log "running the compiled copy against the emulator"
  serve "$unit_dir" "$language" "$target" "$PORT_B" "compiled" \
    "$bindings CLOUDCC_AWS_ENDPOINT_URL=$CLOUDCC_EMULATOR_ENDPOINT" \
    "$case_dir/compiled.log"
  replay "$PORT_B" "$scenario" "$case_dir/compiled.txt"
  stop_app
  trace_from_log "$case_dir/compiled.log" "$case_dir/compiled.trace"
  pass "$example: compiled run recorded"

  # A container unit is provisioned by everything above and *run* by none of it.
  #
  # `pulumi up` creating an ECS service says the service exists, not that a task
  # started, pulled its image and answered anything -- and until this example
  # could be deployed at all, no container unit had ever been started anywhere
  # in this suite. So the service is asked how many tasks are running, and the
  # load balancer in front of it is asked for a response.
  # Cleared first. These are plain shell variables in a loop over every example,
  # so without this an example with no container unit inherits the previous
  # one's service and is asked how many tasks it is running -- which is how
  # `mixed`, whose two units are both functions, failed with "the container
  # never started".
  ecs_cluster=""
  ecs_service=""

  # Matched to this application by name. Listing clusters and taking the first
  # would pass on one another example left behind, which is the kind of check
  # that reports success for something it never looked at.
  ecs_cluster="$(aws_local ecs list-clusters --query 'clusterArns[]' --output text 2>/dev/null \
    | tr '\t' '\n' | grep -- "/$example-" | head -1 || true)"
  if [ -n "$ecs_cluster" ] && [ "$ecs_cluster" != "None" ]; then
    ecs_service="$(aws_local ecs list-services --cluster "$ecs_cluster" \
      --query 'serviceArns[0]' --output text 2>/dev/null || true)"
  fi
  if [ -n "${ecs_service:-}" ] && [ "$ecs_service" != "None" ]; then
    running=0
    for _ in $(seq 1 60); do
      running="$(aws_local ecs describe-services --cluster "$ecs_cluster" \
        --services "$ecs_service" --query 'services[0].runningCount' --output text 2>/dev/null || echo 0)"
      [ "${running:-0}" -ge 1 ] && break
      sleep 2
    done
    [ "${running:-0}" -ge 1 ] \
      || fail "$example: the ECS service has $running running tasks; the container never started"
    pass "$example: L5 a Fargate task is running"

    # Through the load balancer, which is the only address a caller has. A
    # service with a running task behind a target group that never registered
    # it looks healthy from the ECS API and answers nothing.
    alb_host="$(cd "$app_out_dir" && PULUMI_BACKEND_URL="file://$app_out_dir/.pulumi-state" \
      PULUMI_CONFIG_PASSPHRASE=cloudcc-emulator \
      pulumi stack output --json --stack local 2>/dev/null \
      | jq -r 'to_entries[] | select(.key | endswith("Url")) | .value' \
      | grep -vi 'execute-api\|s3-website\|cloudfront\|/sqs\.' | head -1)"
    if [ -n "$alb_host" ]; then
      if wait_for_http "http://${alb_host#http://}/health" 60; then
        pass "$example: L5 the load balancer routes to the task"
      else
        warn "$example: the load balancer at $alb_host did not answer. The task is
  running and reachable inside the emulator's network; whether an address it hands
  out is routable from here is a property of the emulator, not of the deployment."
      fi
    fi
  fi

  # A queue-backed topic is delivered by a poller, not by the queue.
  #
  # `pulumi up` creating a queue says the queue exists. What makes a message
  # reach a subscriber is an event source mapping that Lambda has actually
  # enabled -- and a mapping created before its role policy landed is refused,
  # or created and left disabled, either of which leaves a stack that looks
  # complete and delivers nothing. Nothing else in this run would notice: the
  # api's responses do not depend on the worker, so both halves agree while the
  # asynchronous half is dead. (tests/e2e/load.sh proves delivery itself, by
  # counting the subscriber's invocations.)
  for queue_url in $(aws_local sqs list-queues --query 'QueueUrls[]' --output text 2>/dev/null \
    | tr '\t' '\n' | grep -- "/$example-" || true); do
    queue_arn="$(aws_local sqs get-queue-attributes --queue-url "$queue_url" \
      --attribute-names QueueArn --query 'Attributes.QueueArn' --output text 2>/dev/null || true)"
    [ -n "$queue_arn" ] && [ "$queue_arn" != "None" ] || continue
    mapping_state="$(aws_local lambda list-event-source-mappings \
      --event-source-arn "$queue_arn" \
      --query 'EventSourceMappings[0].State' --output text 2>/dev/null || true)"
    case "$mapping_state" in
      Enabled|Creating)
        pass "$example: L5 $(basename "$queue_url") is polled by an event source mapping ($mapping_state)" ;;
      *)
        fail "$example: the queue $(basename "$queue_url") has no enabled event source mapping (state: ${mapping_state:-none}); nothing would ever consume it" ;;
    esac
  done

  # A CDN-fronted static unit is only fronted if the distribution serves the
  # objects. The bucket is private and reached through an origin access
  # identity, so a distribution that exists with a broken origin returns 403
  # for everything -- and, exactly like the queue above, no other check in this
  # run would see it.
  #
  # What this does not check is that the bucket is private, because the
  # emulator does not enforce bucket policies: an unsigned GET straight at the
  # object succeeds here and would not against AWS. That the origin access
  # identity is the only grant is pinned by a unit test instead
  # (TestACDNBackedSiteGrantsReadsToItsOriginAccessIdentityAlone).
  cdn_domain="$(aws_local cloudfront list-distributions \
    --query "DistributionList.Items[?contains(Comment, 'cloudcc $example ')].DomainName" \
    --output text 2>/dev/null | tr '\t' '\n' | head -1 || true)"
  if [ -n "$cdn_domain" ] && [ "$cdn_domain" != "None" ]; then
    # Addressed through the emulator's own endpoint with the distribution's
    # hostname, which is how a caller outside the emulator's network reaches it.
    cdn_status="$(curl -s -o /dev/null -w '%{http_code}' -m 30 \
      -H "Host: $cdn_domain" "$CLOUDCC_EMULATOR_ENDPOINT/" 2>/dev/null || true)"
    if [ "$cdn_status" = "200" ]; then
      pass "$example: L5 the CDN at $cdn_domain serves the site's index document"
    else
      fail "$example: the distribution at $cdn_domain answered $cdn_status for the index document"
    fi
  fi

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
    pulumi stack export --stack local 2>/dev/null \
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

  # Every subscriber the program declares must have something that delivers to
  # it.
  #
  # This is the check that would have caught kitchen-sink: its reporter called
  # subscribe(), its docstring said it subscribed, and the deployed stack
  # contained *zero* subscription resources -- because the reporter is a
  # container and a delivery is pushed to a function. The handler had never once
  # been called, in either language, since the example was written, and nothing
  # noticed because the records it should have produced are read by nothing.
  #
  # Counting is what makes it work. "Some subscription exists" would have passed
  # for an application with two subscribers and one subscription, which is the
  # shape a mistake actually takes.
  declared_subs="$("$WORK/cloudcc" --dump-ir "$src" 2>/dev/null     | jq '[.edges[]? | select(.kind=="subscribes")] | length' 2>/dev/null || echo 0)"
  if [ "${declared_subs:-0}" -gt 0 ]; then
    deployed_subs="$(printf '%s\n' "$deployed_types"       | grep -cE 'aws:sns/topicSubscription:|aws:lambda/eventSourceMapping:' || true)"
    [ "${deployed_subs:-0}" = "$declared_subs" ]       || fail "$example declares $declared_subs subscriber(s) and the stack has $deployed_subs
  delivery mechanism(s). A subscriber with nothing delivering to it is a handler
  that is never called, and the only sign is records that never appear."
    pass "$example: every declared subscriber has something delivering to it"
  fi


  # A few resources are named for the service that hosts them and drawn under
  # the thing they are. A VPC, its subnets and its route tables all live in the
  # ec2 API; nobody calls them ec2 resources, and the compiler does not either.
  missing=0
  for service in $(printf '%s\n' "$deployed_types" | cut -d: -f2 | cut -d/ -f1 | sort -u); do
    case "$service" in
      ec2) service=vpc ;;
      # Pulumi's type is aws:lb/loadBalancer:LoadBalancer, because that API
      # serves both application and network balancers. The compiler names the
      # kind after the one it provisions, which is what a reader of the diagram
      # is looking for.
      lb) service=alb ;;
    esac
    # A few kinds are drawn under the thing they are rather than the API that
    # serves them, so one service word can have several spellings. An ECS
    # execution role and a task role are both aws:iam/role:Role and neither is
    # called an IAM role by anyone reading the picture -- which only shows up in
    # an application whose *only* roles are those two, and petstore-ts is the
    # first of those.
    #
    # The count check below is the exhaustive one; this is the coarser net that
    # says which service went missing, so widening it here loses nothing.
    alternatives="aws_${service}"
    case "$service" in
      iam) alternatives="aws_iam aws_ecs_execrole aws_ecs_taskrole aws_eks_clusterrole aws_eks_noderole" ;;
    esac
    found=0
    for node in $alternatives; do
      grep -q "$node" "$app_out_dir/architecture.mmd" && { found=1; break; }
    done
    [ "$found" = 1 ] \
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
  "$WORK/cloudcc" deploy "$src" -o "$out" --stack local --destroy >/dev/null
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

  # ---------------------------------------------------------- the same *work*
  #
  # Everything above compares what the two halves answered. This compares what
  # they did to get there: every call through a persisted client, grouped by
  # resource, in order.
  #
  # The two are not the same question, and the second is the one the compiler
  # is actually judged on. A rewrite that pointed a unit at the wrong table, or
  # dropped a publish, or read a secret that resolved to "", can answer every
  # request identically -- and did, in the mutation checks that prove this
  # comparison works.
  trace_normalize "$case_dir/uncompiled.trace" \
    "$case_dir/uncompiled.seams" "$case_dir/uncompiled.async"
  trace_normalize "$case_dir/compiled.trace" \
    "$case_dir/compiled.seams" "$case_dir/compiled.async"

  if [ ! -s "$case_dir/uncompiled.seams" ] && [ ! -s "$case_dir/compiled.seams" ]; then
    # Zero events on both sides is not agreement, it is a comparison that did
    # not happen. Every example in this suite persists something, so this means
    # the tracer did not run -- and a check that silently measures nothing is
    # the failure mode this file already learned once, the hard way.
    failures=$((failures + 1))
    printf '\033[1;31mFAIL\033[0m %s: no seam events were recorded in either half; the trace is not working\n' \
      "$example"
  elif diff -u "$case_dir/uncompiled.seams" "$case_dir/compiled.seams" \
       > "$case_dir/seams.diff"; then
    seam_count="$(grep -c '^   ' "$case_dir/uncompiled.seams" || true)"
    pass "$example: both halves did the same $seam_count operation(s) at their stores"
  else
    failures=$((failures + 1))
    printf '\033[1;31mFAIL\033[0m %s: the two halves answered alike but did different work\n' "$example"
    cat "$case_dir/seams.diff"
  fi
done

echo
log "$checked example(s) compared, $skipped skipped with a reason, $failures failure(s)"
[ "$failures" -eq 0 ] || exit 1
pass "every deployable example behaves identically before and after compiling"
