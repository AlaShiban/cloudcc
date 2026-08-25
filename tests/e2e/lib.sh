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
