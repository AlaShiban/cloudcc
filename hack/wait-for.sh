#!/usr/bin/env bash
# Wait for an HTTP endpoint to answer. Used by the integration harness so a
# slow-starting emulator is a delay rather than a failure.
set -euo pipefail

url="${1:?usage: wait-for.sh <url> [timeout-seconds]}"
timeout="${2:-60}"

waited=0
while [ "$waited" -lt "$timeout" ]; do
  if curl -sf -m 2 -o /dev/null "$url"; then
    exit 0
  fi
  sleep 1
  waited=$((waited + 1))
done

echo "wait-for.sh: $url did not answer within ${timeout}s" >&2
exit 1
