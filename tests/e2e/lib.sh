#!/usr/bin/env bash
# Shared helpers for the integration harness.
#
# The emulator endpoint is always read from $MINISTACK_ENDPOINT and never
# hardcoded, so LocalStack or a remote emulator can be substituted by setting
# one variable.

: "${MINISTACK_ENDPOINT:=http://localhost:4566}"
export MINISTACK_ENDPOINT

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
CLOUDCC_E2E_SERVICES=(dynamodb s3 sns lambda apigatewayv2 apigateway secretsmanager iam sts cloudwatchlogs logs)

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m warn\033[0m %s\n' "$*" >&2; }
fail() { printf '\033[1;31mFAIL\033[0m %s\n' "$*" >&2; exit 1; }
pass() { printf '\033[1;32m ok\033[0m %s\n' "$*"; }

aws_local() { aws --endpoint-url "$MINISTACK_ENDPOINT" --region "$AWS_REGION" "$@"; }

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

ensure_local_bucket() {
  aws_local s3api create-bucket --bucket "$1" >/dev/null 2>&1 || true
}

# The environment a program as written needs to reach the emulator.
# AWS_ENDPOINT_URL is the AWS SDKs' own variable, honoured by boto3 and by the
# JavaScript v3 clients, so nothing cloudcc-specific is involved.
local_aws_env() {
  printf 'AWS_ENDPOINT_URL=%s;AWS_REGION=%s;AWS_DEFAULT_REGION=%s;AWS_ACCESS_KEY_ID=%s;AWS_SECRET_ACCESS_KEY=%s' \
    "$MINISTACK_ENDPOINT" "$AWS_REGION" "$AWS_REGION" \
    "${AWS_ACCESS_KEY_ID:-cloudcc-local}" "${AWS_SECRET_ACCESS_KEY:-cloudcc-local}"
}

# require_endpoint aborts unless something is answering at the emulator.
require_endpoint() {
  if ! curl -sf -m 5 -o /dev/null "$MINISTACK_ENDPOINT" 2>/dev/null; then
    fail "no AWS emulator answering at $MINISTACK_ENDPOINT
  start one with: docker run -d -p 4566:4566 -v /var/run/docker.sock:/var/run/docker.sock ministackorg/ministack
  or point MINISTACK_ENDPOINT at an existing one"
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
  warn "SKIP: the emulator at $MINISTACK_ENDPOINT does not answer $service; this assertion was not run"
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
  pulumi config set aws:skipRequestingAccountId true --stack "$stack" >/dev/null
  pulumi config set aws:s3UsePathStyle true --stack "$stack" >/dev/null
  local service
  for service in "${CLOUDCC_E2E_SERVICES[@]}"; do
    pulumi config set --plaintext --path "aws:endpoints[0].$service" "$MINISTACK_ENDPOINT" --stack "$stack" >/dev/null
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
