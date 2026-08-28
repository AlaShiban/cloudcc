#!/usr/bin/env bash
# Shared helpers for the integration harness.
#
# The emulator endpoint is always read from $CLOUDCC_EMULATOR_ENDPOINT and never
# hardcoded, so LocalStack or a remote emulator can be substituted by setting
# one variable.

: "${CLOUDCC_EMULATOR_ENDPOINT:=http://localhost:4566}"
export CLOUDCC_EMULATOR_ENDPOINT

export AWS_ACCESS_KEY_ID="${AWS_ACCESS_KEY_ID:-test}"
export AWS_SECRET_ACCESS_KEY="${AWS_SECRET_ACCESS_KEY:-test}"
export AWS_REGION="${AWS_REGION:-us-east-1}"
export AWS_DEFAULT_REGION="$AWS_REGION"

# Pulumi runs against a local filesystem backend so the tests need no account.
export PULUMI_CONFIG_PASSPHRASE="${PULUMI_CONFIG_PASSPHRASE:-cloudcc-test}"
export PULUMI_SKIP_UPDATE_CHECK=true

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
export REPO_ROOT

# The AWS services the harness points at the emulator. Each becomes one
# aws:endpoints[0].<service> setting.
#
# This list must match deploy.EmulatedServices, and a Go test compares them.
# They drifted once and it was expensive to read: `cloudcc deploy` configures
# its own endpoints, so the differential suite deployed an RDS instance
# happily, while a harness that drives pulumi directly sent the availability
# zone lookup to real AWS and failed with "AuthFailure: AWS was not able to
# validate the provided access credentials" -- which looks like a credentials
# problem and is a missing endpoint.
CLOUDCC_E2E_SERVICES=(apigateway apigatewayv2 cloudwatch cloudwatchlogs dynamodb \
  ec2 ecr ecs eks elasticache elbv2 iam lambda logs \
  memorydb rds s3 secretsmanager sns sts)

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m warn\033[0m %s\n' "$*" >&2; }
fail() { printf '\033[1;31mFAIL\033[0m %s\n' "$*" >&2; exit 1; }
pass() { printf '\033[1;32m ok\033[0m %s\n' "$*"; }

aws_local() { aws --endpoint-url "$CLOUDCC_EMULATOR_ENDPOINT" --region "$AWS_REGION" "$@"; }

# app_name reads `app:` out of a source tree's cloudcc.yaml.
#
# out_dir holds a folder per application, so the artefacts of a compile with
# `-o out` are at out/<app>. `-o` still names the parent -- it is the shared
# root, not one app's output -- which is why the two paths are different and
# why this helper exists.
app_name() {
  local src="$1"
  local name=""
  if [ -f "$src/cloudcc.yaml" ]; then
    name="$(sed -n 's/^app:[[:space:]]*//p' "$src/cloudcc.yaml" | head -1 | tr -d '"'"'"' \r')"
  fi
  [ -n "$name" ] || name="$(basename "$src")"
  printf '%s' "$name"
}

# app_out prints where a compile with `-o <out>` put <src>'s artefacts.
app_out() { printf '%s/%s' "$1" "$(app_name "$2")"; }

# ---------------------------------------------------------------- the program
#
# Which module holds the application, what the binding is called, and which
# language it is in are all facts the compiler already worked out. Asking it
# beats guessing: the harness used to hardcode `uvicorn app:app`, which is
# right for one example and wrong for every program whose entry is not app.py.

# unit_field <cloudcc-binary> <src> <unit> <jq-expr-on-.payload>
unit_field() {
  local bin="$1" src="$2" unit="$3" expr="$4"
  "$bin" --dump-ir "$src" 2>/dev/null \
    | jq -r --arg unit "$unit" \
        "[.intents[] | select(.key.kind==\"execution_unit\") | select(.payload.id==\$unit)][0] | $expr" \
    2>/dev/null || true
}

# unit_language prints "python" or "node".
unit_language() { unit_field "$1" "$2" "$3" '.payload.language // "python"'; }

# unit_target prints what a server needs to load the unit:
#   python -> "package.module:appvar"   (a dotted module, as uvicorn wants)
#   node   -> "relative/file.js:appvar"
unit_target() {
  local entry app
  entry="$(unit_field "$1" "$2" "$3" '.payload.entrypoints[0]')"
  app="$(unit_field "$1" "$2" "$3" '.payload.asgi_app')"
  [ -n "$entry" ] && [ "$entry" != "null" ] || return 1
  [ -n "$app" ] && [ "$app" != "null" ] || return 1
  if [ "$(unit_language "$1" "$2" "$3")" = "node" ]; then
    printf '%s:%s' "$entry" "$app"
    return 0
  fi
  printf '%s:%s' "$(printf '%s' "${entry%.py}" | tr '/' '.')" "$app"
}

# ------------------------------------------------------------------ the store
#
# A program as written now holds a real client -- a boto3 Table, a
# DynamoDBClient -- rather than a class the SDK supplied, so "run it as
# written" means running it against a real store. The emulator is already up
# for the compiled half, so it serves both, under the *local* name the program
# wrote rather than the physical one the compiler chose.

reset_local_table() {
  aws_local dynamodb delete-table --table-name "$1" >/dev/null 2>&1 || true
  aws_local dynamodb create-table --table-name "$1" \
    --attribute-definitions AttributeName=id,AttributeType=S \
    --key-schema AttributeName=id,KeyType=HASH \
    --billing-mode PAY_PER_REQUEST >/dev/null \
    || fail "could not create the local table $1 in the emulator"
}

# ensure_local_bucket creates the bucket a program-as-written expects, and
# empties it.
#
# Emptying matters as much as creating. reset_local_table drops and recreates
# its table for exactly this reason, and a bucket left alone is the same bug:
# the uncompiled half reads objects a previous run wrote, while the compiled
# half reads a bucket the compiler has just provisioned and is therefore empty.
# The two runs then differ on the first read -- 200 against 404 -- for a reason
# that has nothing to do with compiling, and the diff points at the example.
ensure_local_bucket() {
  aws_local s3api create-bucket --bucket "$1" >/dev/null 2>&1 || true
  aws_local s3 rm "s3://$1" --recursive >/dev/null 2>&1 || true
}

# The environment a program as written needs to reach the emulator.
# AWS_ENDPOINT_URL is the AWS SDKs' own variable, honoured by boto3 and by the
# JavaScript v3 clients, so nothing cloudcc-specific is involved.
local_aws_env() {
  printf 'AWS_ENDPOINT_URL=%s;AWS_REGION=%s;AWS_DEFAULT_REGION=%s;AWS_ACCESS_KEY_ID=%s;AWS_SECRET_ACCESS_KEY=%s' \
    "$(app_endpoint)" "$AWS_REGION" "$AWS_REGION" \
    "${AWS_ACCESS_KEY_ID:-cloudcc-local}" "${AWS_SECRET_ACCESS_KEY:-cloudcc-local}"
}

# app_endpoint is the emulator's address as an application should be given it:
# by IP rather than by name.
#
# This decides how the AWS SDKs address S3 buckets, which is not a detail. Given
# a host*name*, the JavaScript SDK uses virtual-host addressing and sends the
# bucket as a subdomain -- `pet-photos.localhost` -- which the emulator cannot
# parse, and the write fails with "unknown operation ... invalid XML received",
# a message about neither buckets nor addressing. Given an IP it uses path
# style, `/pet-photos/key`, which every emulator understands.
#
# The compiled half does not depend on this: its injected client sets
# forcePathStyle explicitly. The uncompiled half is the program as the user
# wrote it, so there is nowhere to set that -- which is the whole point of
# running it, and why the endpoint has to carry the hint instead.
#
# AWS_S3_FORCE_PATH_STYLE is not an answer: the JavaScript SDK does not read it.
app_endpoint() {
  printf '%s' "${CLOUDCC_EMULATOR_ENDPOINT/localhost/127.0.0.1}"
}

# emulator_python_target prints "<platform> <version>" for the Lambda runtime
# the emulator actually uses, by deploying a three-line function and asking it.
#
# It has to be asked rather than assumed. The emulator runs containers of
# whatever the host is, with whatever Python it has, regardless of the
# architecture and runtime the function declares -- so a bundle built for what
# a real deployment wants cannot be imported here. A compiled dependency then
# fails with "No module named pydantic_core._pydantic_core", which says nothing
# about the actual cause, so guessing wrong is expensive to diagnose.
emulator_python_target() {
  local dir probe
  dir="$(mktemp -d "${TMPDIR:-/tmp}/cloudcc-probe-XXXXXX")"
  # sysconfig's SOABI is the authority: it is the exact tag a compiled
  # extension's filename has to carry to be importable here, libc included.
  cat > "$dir/probe.py" <<'PROBE'
import platform, sys, sysconfig
def handler(event, context):
    return {"machine": platform.machine(),
            "python": "%d.%d" % sys.version_info[:2],
            "soabi": sysconfig.get_config_var("SOABI") or ""}
PROBE
  ( cd "$dir" && zip -q probe.zip probe.py )

  aws_local lambda delete-function --function-name cloudcc-target-probe >/dev/null 2>&1 || true
  aws_local lambda create-function --function-name cloudcc-target-probe \
    --runtime python3.12 --handler probe.handler \
    --role arn:aws:iam::000000000000:role/cloudcc-probe \
    --zip-file "fileb://$dir/probe.zip" >/dev/null 2>&1 || { rm -rf "$dir"; return 1; }

  local waited=0
  while [ "$waited" -lt 30 ]; do
    if aws_local lambda invoke --function-name cloudcc-target-probe \
         --cli-binary-format raw-in-base64-out --payload '{}' \
         "$dir/out.json" >/dev/null 2>&1 && [ -s "$dir/out.json" ] \
         && jq -e '.machine' "$dir/out.json" >/dev/null 2>&1; then
      break
    fi
    sleep 2
    waited=$((waited + 2))
  done
  aws_local lambda delete-function --function-name cloudcc-target-probe >/dev/null 2>&1 || true

  if ! jq -e '.machine' "$dir/out.json" >/dev/null 2>&1; then
    rm -rf "$dir"
    return 1
  fi
  local machine version soabi
  machine="$(jq -r '.machine' "$dir/out.json")"
  version="$(jq -r '.python' "$dir/out.json")"
  soabi="$(jq -r '.soabi' "$dir/out.json")"
  rm -rf "$dir"

  # Real Lambda is Amazon Linux, which is glibc; this emulator turns out to run
  # Alpine, which is musl. A manylinux wheel cannot be loaded on musl at all --
  # different dynamic linker -- so this is not a detail that can be skipped.
  local libc="manylinux2014"
  case "$soabi" in
    *musl*) libc="unknown-linux-musl" ;;
  esac

  case "$machine" in
    arm64|aarch64) printf 'aarch64-%s %s' "$libc" "$version" ;;
    *)             printf 'x86_64-%s %s' "$libc" "$version" ;;
  esac
}

# ------------------------------------------------------------- real engines
#
# The emulator provisions an RDS instance and an ElastiCache cluster -- the
# resources appear, the stack exports their bindings -- but it does not run a
# Postgres or a Redis behind them. Nothing would connect.
#
# So the engines are real, in Docker, the same way the emulator is real for
# everything else: a stand-in for the managed service, not a mock of it. A
# program that talks SQL talks it to an actual Postgres, and a cache miss is an
# actual cache miss.
#
# The credentials and database name are not arbitrary. The compiler emits
# `postgresql://ccadmin@<address>:<port>/<db>` with no password, because on AWS
# the master credential is managed and the shim splices it in from the secret.
# An emulator reports no managed secret, so the URL is used as written -- which
# means the container has to be the user and database that URL names, and to
# trust the connection. Anything else and the compiled half would need its
# binding overridden, which would stop testing the binding.

#: Container names are fixed so a rerun reuses what is already up rather than
#: paying the start-up cost again, and so a stray one is easy to find.
CLOUDCC_PG_CONTAINER="${CLOUDCC_PG_CONTAINER:-cloudcc-postgres}"
CLOUDCC_REDIS_CONTAINER="${CLOUDCC_REDIS_CONTAINER:-cloudcc-redis}"

# ensure_engine <postgres|redis> [database]
ensure_engine() {
  local kind="$1" database="${2:-petsdb}"
  case "$kind" in
    postgres)
      if [ "$(docker inspect -f '{{.State.Running}}' "$CLOUDCC_PG_CONTAINER" 2>/dev/null)" != "true" ]; then
        docker rm -f "$CLOUDCC_PG_CONTAINER" >/dev/null 2>&1 || true
        docker run -d --name "$CLOUDCC_PG_CONTAINER" -p 5432:5432 \
          -e POSTGRES_USER=ccadmin \
          -e POSTGRES_DB="$database" \
          -e POSTGRES_HOST_AUTH_METHOD=trust \
          postgres:16-alpine >/dev/null 2>&1 \
          || fail "could not start Postgres; is Docker running?"
      fi
      # Readiness is a successful query over TCP, not pg_isready.
      #
      # The official image runs its initialisation against a temporary server
      # that listens on the unix socket *only*, and then restarts. pg_isready
      # answers "accepting connections" during that window, so a command issued
      # on the strength of it hits a server that is about to go away. On CI this
      # failed 1.5 seconds after the container was created, which is not long
      # enough for Postgres to have started at all; on a desktop where the
      # container had been up for hours it never appeared.
      #
      # `-h 127.0.0.1` is the part that matters, and a query over the socket
      # would not do: the socket is exactly what the initialisation server
      # listens on, so a socket query answers "yes" during the window this is
      # trying to wait out. TCP is also how every program under test reaches it.
      local waited=0
      until docker exec "$CLOUDCC_PG_CONTAINER" \
              psql -h 127.0.0.1 -U ccadmin -d postgres -tAc 'SELECT 1' >/dev/null 2>&1; do
        waited=$((waited + 1))
        [ "$waited" -gt 90 ] && fail "Postgres did not accept a TCP query in 90s"
        sleep 1
      done

      # POSTGRES_DB creates one database, and only on a container's first
      # start. An application may declare several -- petstore-multi has an
      # async engine on petsdb and a synchronous one on auditdb -- and the
      # container is deliberately reused between runs, so the rest are created
      # here. CREATE DATABASE has no IF NOT EXISTS, hence the guard.
      #
      # Retried because two harnesses may create the same database at once, and
      # because the check and the create are not atomic: losing that race
      # returns "already exists", which is the state being asked for.
      local tries=0
      until docker exec "$CLOUDCC_PG_CONTAINER" psql -h 127.0.0.1 -U ccadmin -d postgres -tAc \
              "SELECT 1 FROM pg_database WHERE datname='$database'" 2>/dev/null | grep -q 1; do
        docker exec "$CLOUDCC_PG_CONTAINER" psql -h 127.0.0.1 -U ccadmin -d postgres -q \
          -c "CREATE DATABASE \"$database\"" >/dev/null 2>&1 || true
        tries=$((tries + 1))
        [ "$tries" -gt 10 ] && fail "could not create the $database database"
        sleep 1
      done
      ;;
    redis)
      if [ "$(docker inspect -f '{{.State.Running}}' "$CLOUDCC_REDIS_CONTAINER" 2>/dev/null)" != "true" ]; then
        docker rm -f "$CLOUDCC_REDIS_CONTAINER" >/dev/null 2>&1 || true
        docker run -d --name "$CLOUDCC_REDIS_CONTAINER" -p 6379:6379 redis:7-alpine >/dev/null 2>&1 \
          || fail "could not start Redis; is Docker running?"
      fi
      local rwaited=0
      until docker exec "$CLOUDCC_REDIS_CONTAINER" redis-cli ping >/dev/null 2>&1; do
        rwaited=$((rwaited + 1))
        [ "$rwaited" -gt 60 ] && fail "Redis did not become ready in 60s"
        sleep 1
      done
      ;;
    *) fail "unknown engine $kind" ;;
  esac
}

# scenario_databases <scenario.json> -- the Postgres databases it needs.
#
# From the scenario rather than a list here, because which databases exist is a
# property of the example: examples/mixed declares one called "shop" and
# petstore-multi declares two. Defaulting to petsdb keeps the scenarios that
# predate this from having to say anything.
scenario_databases() {
  jq -r '[(.databases // ["petsdb"]), (.load.databases // [])] | flatten | unique | .[]' \
    "$1" 2>/dev/null
}

# ensure_engines <scenario.json> -- starts whatever the scenario declares.
ensure_engines() {
  local scenario="$1" engine database
  for engine in $(jq -r '[(.engines // []), (.load.engines // [])] | flatten | .[]' "$scenario" 2>/dev/null); do
    log "starting $engine"
    if [ "$engine" = postgres ]; then
      for database in $(scenario_databases "$scenario"); do
        ensure_engine postgres "$database"
      done
    else
      ensure_engine "$engine"
    fi
  done
}

# reset_engines wipes their contents without restarting them, so one example's
# rows are not another's evidence.
reset_engines() {
  local scenario="$1" engine database
  for engine in $(jq -r '[(.engines // []), (.load.engines // [])] | flatten | .[]' "$scenario" 2>/dev/null); do
    case "$engine" in
      postgres)
        for database in $(scenario_databases "$scenario"); do
          docker exec "$CLOUDCC_PG_CONTAINER" psql -U ccadmin -d "$database" -q \
            -c 'DROP SCHEMA public CASCADE; CREATE SCHEMA public;' >/dev/null 2>&1 || true
        done
        ;;
      redis)
        docker exec "$CLOUDCC_REDIS_CONTAINER" redis-cli FLUSHALL >/dev/null 2>&1 || true
        ;;
    esac
  done
}

# stop_engines removes them. Not called on every exit: they take a few seconds
# to start and are reused across runs.
stop_engines() {
  docker rm -f "$CLOUDCC_PG_CONTAINER" "$CLOUDCC_REDIS_CONTAINER" >/dev/null 2>&1 || true
}

# Point the managed-engine bindings at the containers that actually serve them.
#
# The emulator provisions an RDS instance and an ElastiCache cluster and reports
# where they are *on its own network*: a container IP on CI, and localhost on a
# desktop Docker that publishes ports to the host. Neither is where the engine
# is, because the emulator runs no engine -- so both are redirected, and neither
# is a statement about the compiler.
#
# It is worth being exact about what that costs. The URL's scheme, user and
# database name are the compiler's and are used as emitted; only the host and
# port are replaced. So the shape of the binding is still tested and the address
# in it is not, which is the most that can be checked against a control plane
# with nothing behind it.
#
# This was localhost-only at first, which worked on the machine it was written
# on and failed on CI with "fe_sendauth: no password supplied" -- the compiled
# program had reached the emulator's own address and been asked to authenticate.

#: Where the real engines listen, from the harness's point of view.
CLOUDCC_PG_HOSTPORT="${CLOUDCC_PG_HOSTPORT:-127.0.0.1:5432}"
CLOUDCC_REDIS_HOST="${CLOUDCC_REDIS_HOST:-127.0.0.1}"

# engine_bindings_local <bindings-string> -- echoes it with the engines redirected.
engine_bindings_local() {
  printf '%s' "$1" \
    | sed -E "s/(CLOUDCC_REDIS_[A-Z0-9_]+_ENDPOINT)=[^ ]*/\1=$CLOUDCC_REDIS_HOST/g" \
    | sed -E "s#(CLOUDCC_ORM_[A-Z0-9_]+_URL=[a-z+]+://[^@ ]*@)[^/ ]+#\1$CLOUDCC_PG_HOSTPORT#g"
}

# export_engine_bindings_local -- the same, for a harness that exports them.
export_engine_bindings_local() {
  local name value
  for name in $(env | sed -n -E 's/^(CLOUDCC_REDIS_[A-Z0-9_]+_ENDPOINT)=.*/\1/p'); do
    export "$name=$CLOUDCC_REDIS_HOST"
  done
  for name in $(env | sed -n -E 's/^(CLOUDCC_ORM_[A-Z0-9_]+_URL)=.*/\1/p'); do
    value="$(printf '%s' "${!name}" \
      | sed -E "s#^([a-z+]+://[^@]*@)[^/]+#\1$CLOUDCC_PG_HOSTPORT#")"
    export "$name=$value"
  done
}

# seed_stack_config gives every config value the program declares without one.
#
# A `config_value` with no default is required, and the generated program calls
# require() for it -- so `pulumi up` refuses before creating anything, with
# "Missing required configuration variable". That is correct: the value is an
# operator's to supply, and a compiler that invented one would be putting a
# secret in a repository, which is the thing D21 exists to prevent.
#
# So the harness does what an operator would, once, before deploying. Values are
# obvious placeholders: nothing asserts on them, and a real-looking secret in a
# test is a secret somebody eventually copies.
seed_stack_config() {
  local bin="$1" src="$2" app_out="$3" stack="$4" name required

  # Asked of the compiler rather than parsed out of YAML. The harness already
  # gets every other fact this way, and a shell parser for "a name under
  # config: with no value: of its own" is a parser -- indentation is what
  # distinguishes an entry from a key inside one, and getting that wrong means
  # setting the wrong variable or none.
  required="$("$bin" --dump-ir "$src" 2>/dev/null \
    | jq -r '.intents[] | select(.key.kind=="config") | select(has("payload"))
             | select((.payload.default // "") == "") | .payload.id' 2>/dev/null || true)"
  [ -n "$required" ] || return 0

  # In the same backend `cloudcc deploy` will use, which is a directory beside
  # the generated project unless the environment names one. Setting config in a
  # different backend creates a stack somewhere else and leaves the deploy
  # reporting "no stack selected" -- a message about the workspace rather than
  # about the config that is actually missing.
  local backend="${PULUMI_BACKEND_URL:-file://$app_out/.pulumi-state}"
  mkdir -p "$app_out/.pulumi-state"

  (
    cd "$app_out" || exit 0
    export PULUMI_BACKEND_URL="$backend"
    export PULUMI_CONFIG_PASSPHRASE="${PULUMI_CONFIG_PASSPHRASE:-cloudcc-test}"
    pulumi stack init "$stack" >/dev/null 2>&1 || pulumi stack select "$stack" >/dev/null 2>&1 || true
    for name in $required; do
      pulumi config set --plaintext "cloudcc:$name" "cloudcc-e2e-$name" \
        --stack "$stack" >/dev/null 2>&1 || true
    done
  )
}

# warm_emulator_rds makes the emulator install its database engine before a test
# needs one.
#
# LocalStack installs PostgreSQL on first use, and the instance that triggers
# the install can fail while it is still running -- the failure is
# `Unable to startup DB instance: [SSL] PEM lib`, which names neither the
# install nor the reason. On a container that has served an instance before, the
# engine is already there and nothing goes wrong, which is why this passes on a
# developer machine that has run the suite once and fails on CI's fresh
# container every time.
#
# So the first instance is created here, deliberately and out of band, where a
# failure costs nothing and is not blamed on the application under test.
# Idempotent: a container that already has the engine finishes this in a second.
warm_emulator_rds() {
  local name="cloudcc-rds-warmup" waited=0 status
  aws_local rds describe-db-instances --db-instance-identifier "$name" >/dev/null 2>&1     || aws_local rds create-db-instance          --db-instance-identifier "$name"          --db-instance-class db.t3.micro          --engine postgres          --master-username warmup          --master-user-password warmup-password          --allocated-storage 5 >/dev/null 2>&1     || return 0

  until [ "$waited" -gt 180 ]; do
    status="$(aws_local rds describe-db-instances --db-instance-identifier "$name"       --query 'DBInstances[0].DBInstanceStatus' --output text 2>/dev/null || true)"
    case "$status" in
      available) break ;;
      error|failed) break ;;
    esac
    waited=$((waited + 1))
    sleep 1
  done
  aws_local rds delete-db-instance --db-instance-identifier "$name"     --skip-final-snapshot >/dev/null 2>&1 || true
}

# seed_secrets gives every provisioned secret a value.
#
# The compiler provisions the secret and deliberately not its contents: a value
# in the generated project would be a value in Pulumi state and in the
# repository, which is the thing D21 exists to prevent. Setting it is an
# operator's job, done once, out of band -- so the harness does what an operator
# would, and a run that skipped it would be testing an application nobody had
# finished deploying.
#
# Reading a secret that has no version raises, and that is the correct
# behaviour; it is just not what this test is trying to find out.
#: The value every secret gets in a test. Named once because the two halves
#: have to agree on it: the compiled program reads it from Secrets Manager and
#: the uncompiled one from the environment, and a test that compares them is
#: comparing this constant with itself.
CLOUDCC_E2E_SECRET="cloudcc-emulator-secret"

# secret_env prints the environment a program-as-written needs to read the same
# secrets its deployed counterpart will.
#
# A `cloudcc.Secret()` with no source of its own reads CLOUDCC_SECRET_<ID>
# locally -- the same name the compiled binding is given -- so supplying it here
# is what lets a differential test include a route that uses one. Without it the
# local half reads an empty string and the two can never match.
secret_env() {
  local bin="$1" src="$2" id
  for id in $("$bin" --dump-ir "$src" 2>/dev/null \
      | jq -r '.intents[] | select(.key.kind=="persist_secret") | .key.id' 2>/dev/null); do
    printf 'CLOUDCC_SECRET_%s=%s;' \
      "$(printf '%s' "$id" | tr -c '[:alnum:]' '_' | tr '[:lower:]' '[:upper:]')" \
      "$CLOUDCC_E2E_SECRET"
  done
}

seed_secrets() {
  local outputs="$1" arn
  for arn in $(printf '%s' "$outputs" \
      | jq -r 'to_entries[] | select(.key | test("^CLOUDCC_SECRET_.*_ARN$")) | .value'); do
    [ -n "$arn" ] || continue
    aws_local secretsmanager put-secret-value \
      --secret-id "$arn" --secret-string "$CLOUDCC_E2E_SECRET" >/dev/null 2>&1 || true
  done
}

# py_run_deps <dir> -- the uv arguments that give a Python unit its dependencies.
#
# From the unit's own requirements.txt rather than a list kept here. A harness
# that hardcodes "fastapi, boto3" quietly limits which capabilities an example
# may use: the first example to declare a Redis client fails to start with
# ModuleNotFoundError, and the fix looks like it belongs in the example.
#
# uvicorn is added separately because it is the harness's, not the
# application's -- nothing in a deployed unit imports it.
py_run_deps() {
  local dir="$1"
  printf -- '--with uvicorn'
  if [ -s "$dir/requirements.txt" ]; then
    printf -- ' --with-requirements %s/requirements.txt' "$dir"
  fi
}

# require_endpoint aborts unless something is answering at the emulator.
require_endpoint() {
  if ! curl -sf -m 5 -o /dev/null "$CLOUDCC_EMULATOR_ENDPOINT" 2>/dev/null; then
    fail "no AWS emulator answering at $CLOUDCC_EMULATOR_ENDPOINT
  start one with: docker run -d -p 4566:4566 -v /var/run/docker.sock:/var/run/docker.sock ministackorg/ministack
  or point CLOUDCC_EMULATOR_ENDPOINT at an existing one"
  fi
}

# probe_service makes one cheap call against a service. A non-2xx answer means
# the emulator does not implement it well enough to test against, so the caller
# skips with a printed reason rather than silently passing.
probe_service() {
  local service="$1"
  case "$service" in
    dynamodb)       aws_local dynamodb list-tables            >/dev/null 2>&1 ;;
    s3)             aws_local s3api list-buckets              >/dev/null 2>&1 ;;
    sns)            aws_local sns list-topics                 >/dev/null 2>&1 ;;
    lambda)         aws_local lambda list-functions           >/dev/null 2>&1 ;;
    apigatewayv2)   aws_local apigatewayv2 get-apis           >/dev/null 2>&1 ;;
    secretsmanager) aws_local secretsmanager list-secrets     >/dev/null 2>&1 ;;
    logs)           aws_local logs describe-log-groups        >/dev/null 2>&1 ;;
    eks)            aws_local eks list-clusters               >/dev/null 2>&1 ;;
    *)              return 1 ;;
  esac
}

# skip_unless_service prints a reason and returns non-zero when a service is
# not usable, so a gap in the emulator never reads as a pass.
skip_unless_service() {
  local service="$1"
  if probe_service "$service"; then
    return 0
  fi
  warn "SKIP: the emulator at $CLOUDCC_EMULATOR_ENDPOINT does not answer $service; this assertion was not run"
  return 1
}

# pulumi_configure_emulator points a stack at the emulator. Path-based keys are
# how the AWS provider's endpoint list is addressed.
pulumi_configure_emulator() {
  local stack="$1"
  pulumi config set aws:region "$AWS_REGION" --stack "$stack" >/dev/null
  # --plaintext throughout: these are throwaway emulator settings, and Pulumi
  # otherwise refuses keys whose names look secret ("secretKey",
  # "endpoints[0].secretsmanager").
  pulumi config set --plaintext aws:accessKey "$AWS_ACCESS_KEY_ID" --stack "$stack" >/dev/null
  pulumi config set --plaintext aws:secretKey "$AWS_SECRET_ACCESS_KEY" --stack "$stack" >/dev/null
  pulumi config set aws:skipCredentialsValidation true --stack "$stack" >/dev/null
  pulumi config set aws:skipMetadataApiCheck true --stack "$stack" >/dev/null
  # Not skipRequestingAccountId. Skipping it leaves the provider with an empty
  # account id, which ministack ignored and LocalStack does not: SNS answers
  # GetTopicAttributes with "'' is not a valid AWS account ID" and the stack
  # fails on its first topic. Both emulators answer STS, so there is nothing to
  # skip -- asking is both faithful and cheap.
  pulumi config set aws:skipRequestingAccountId false --stack "$stack" >/dev/null
  pulumi config set aws:s3UsePathStyle true --stack "$stack" >/dev/null
  local service
  for service in "${CLOUDCC_E2E_SERVICES[@]}"; do
    pulumi config set --plaintext --path "aws:endpoints[0].$service" "$CLOUDCC_EMULATOR_ENDPOINT" --stack "$stack" >/dev/null
  done
}

# wait_for_http polls a URL until it answers or the timeout expires.
wait_for_http() {
  local url="$1" timeout="${2:-30}" waited=0
  while [ "$waited" -lt "$timeout" ]; do
    if curl -sf -m 2 -o /dev/null "$url"; then
      return 0
    fi
    sleep 1
    waited=$((waited + 1))
  done
  return 1
}
