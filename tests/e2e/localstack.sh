#!/usr/bin/env bash
# Start, stop or check the LocalStack emulator this suite runs against.
#
# Every harness reads its endpoint from $CLOUDCC_EMULATOR_ENDPOINT and nothing else, so
# swapping emulators is a matter of what is listening on that port. This script
# starts LocalStack with the four settings that are not optional, each of which
# was found by watching something fail:
#
#   LOCALSTACK_AUTH_TOKEN   EKS, ECS, RDS and ElastiCache are Pro services.
#                           Without a token they report as unavailable and the
#                           stack fails on the first one it reaches.
#
#   the Docker socket       LocalStack runs Lambda in containers and backs EKS
#                           with k3d. Without the socket, EKS answers
#                           create-cluster with an ACTIVE cluster that has no
#                           Kubernetes behind it, and Lambda cannot start at all.
#
#   LAMBDA_IGNORE_ARCHITECTURE
#                           A function declares x86_64 unless told otherwise,
#                           and this machine is arm64. Without this LocalStack
#                           refuses to start the execution environment and the
#                           invoke fails with "Could not start new environment:
#                           ContainerException" -- which names neither
#                           architecture.
#
#   the 4510-4559 range     EKS publishes each cluster's API server on a port
#                           from this range. Publishing only 4566 leaves the
#                           cluster unreachable.
#
#   LOCALSTACK_HOST         By default LocalStack hands out hostnames under
#                           localhost.localstack.cloud, a public name that
#                           resolves to 127.0.0.1 -- and does not resolve at all
#                           where DNS is blocked or absent. Pointing it at
#                           localhost makes ECR hand out
#                           <account>.dkr.ecr.<region>.localhost:4566, and
#                           *.localhost resolves without asking anyone.
#
# Pushing to that registry needs Docker to treat it as insecure, because Docker
# requires HTTPS for any host that is not literally localhost and LocalStack's
# certificate covers a different name. On colima that is:
#
#     # ~/.colima/default/colima.yaml
#     docker:
#       insecure-registries:
#         - "000000000000.dkr.ecr.us-east-1.localhost:4566"
#
# then `colima restart`. Wildcards are rejected by dockerd -- it wants the exact
# host -- and the failure is a daemon that will not start at all.
#
# The token is read from the environment and never written anywhere: not into
# this repository, not into a compose file, not into a container label. Set it
# once in your shell, or in a file outside the repository that you source.
#
# Usage:
#   ./tests/e2e/localstack.sh start|stop|status
set -euo pipefail

CONTAINER="${CLOUDCC_LOCALSTACK_CONTAINER:-localstack-main}"
IMAGE="${CLOUDCC_LOCALSTACK_IMAGE:-localstack/localstack-pro:latest}"
ENDPOINT="${CLOUDCC_EMULATOR_ENDPOINT:-http://localhost:4566}"

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
fail() { printf '\033[1;31mFAIL\033[0m %s\n' "$*" >&2; exit 1; }
pass() { printf '\033[1;32m ok\033[0m %s\n' "$*"; }

case "${1:-status}" in
  start)
    [ -n "${LOCALSTACK_AUTH_TOKEN:-}" ] || fail "LOCALSTACK_AUTH_TOKEN is not set.
  The Pro services this suite needs -- EKS, ECS, RDS, ElastiCache -- are not in
  the community image. Export the token in your shell; do not put it in a file
  in this repository."

    docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
    log "starting $IMAGE as $CONTAINER"
    docker run -d --name "$CONTAINER" \
      -p 4566:4566 -p 4510-4559:4510-4559 \
      -e LOCALSTACK_AUTH_TOKEN \
      -e LAMBDA_IGNORE_ARCHITECTURE=1 \
      -e LOCALSTACK_HOST=localhost:4566 \
      -v /var/run/docker.sock:/var/run/docker.sock \
      "$IMAGE" >/dev/null || fail "could not start $CONTAINER"

    log "waiting for it to answer"
    waited=0
    until curl -s --max-time 3 "$ENDPOINT/_localstack/health" >/dev/null 2>&1; do
      waited=$((waited + 1))
      [ "$waited" -gt 60 ] && fail "LocalStack did not answer within five minutes"
      sleep 5
    done

    # A license that failed to activate leaves every Pro service reporting as
    # available and failing on use, so the log is the thing to check rather than
    # the health endpoint.
    # Waited for, not checked once: the health endpoint answers before licensing
    # has finished, so a single look reports an unlicensed container that is
    # about to be licensed a second later.
    licensed=0
    for _ in $(seq 1 30); do
      if docker logs "$CONTAINER" 2>&1 | grep -q "activated new license"; then
        licensed=1
        break
      fi
      if docker logs "$CONTAINER" 2>&1 | grep -qi "license activation failed"; then
        break
      fi
      sleep 2
    done
    if [ "$licensed" = "1" ]; then
      pass "licensed"
    else
      docker logs "$CONTAINER" 2>&1 | grep -i licens | tail -3
      fail "no license was activated; the Pro services will not work.
  Check the token: docker logs $CONTAINER | grep -i licens"
    fi
    # The database engine is installed on first use, and the instance that
    # triggers the install can fail while it is still running. Doing it here
    # means a test never pays for it.
    log "warming the database engine (installed on first use)"
    # shellcheck source=/dev/null
    . "$(dirname "$0")/lib.sh"
    warm_emulator_rds
    exec "$0" status
    ;;

  stop)
    docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
    # A cluster's k3d containers outlive LocalStack itself.
    for c in $(docker ps -aq --filter "name=k3d-" 2>/dev/null); do
      docker rm -f "$c" >/dev/null 2>&1 || true
    done
    pass "stopped"
    ;;

  status)
    curl -s --max-time 5 "$ENDPOINT/_localstack/health" >/dev/null 2>&1 \
      || fail "nothing is answering at $ENDPOINT"
    log "endpoint: $ENDPOINT"
    curl -s "$ENDPOINT/_localstack/health" | python3 -c '
import json, sys
services = json.load(sys.stdin)["services"]
# The ones this suite actually deploys. Listed rather than printing all two
# hundred, so a missing one is visible rather than buried.
needed = ["apigatewayv2", "dynamodb", "ecr", "ecs", "eks", "elasticache",
          "elbv2", "iam", "lambda", "logs", "rds", "s3", "secretsmanager", "sns", "sts"]
missing = []
for name in needed:
    state = services.get(name, "MISSING")
    print("  %-16s %s" % (name, state))
    if state not in ("available", "running"):
        missing.append(name)
if missing:
    raise SystemExit("\nnot available: " + ", ".join(missing))
' || fail "some services this suite needs are not available"
    pass "every service this suite deploys is available"
    ;;

  *)
    fail "usage: $0 start|stop|status"
    ;;
esac
