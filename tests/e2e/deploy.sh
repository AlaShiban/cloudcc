#!/usr/bin/env bash
# End-to-end test of `cloudcc deploy` itself.
#
# tests/e2e/provisioning.sh drives Pulumi directly, which is what proves the
# generated project is sound. This one goes through cloudcc's own deploy command,
# which is what proves the preflight, the emulator stack configuration and the
# packaging sequence work.
set -euo pipefail

source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

EXAMPLE="${1:-petstore}"
WORK="${CLOUDCC_E2E_WORKDIR:-$(mktemp -d "${TMPDIR:-/tmp}/cloudcc-deploy-XXXXXX")}"
SRC="$WORK/src"
OUT="$WORK/compiled"
KEEP="${CLOUDCC_E2E_KEEP:-0}"

cleanup() {
  local status=$?
  if [ -d "$OUT" ] && [ "${DESTROYED:-0}" != "1" ]; then
    "$WORK/cloudcc" deploy "$SRC" -o "$OUT" --stack local --destroy >/dev/null 2>&1 || true
  fi
  [ "$KEEP" = "0" ] && rm -rf "$WORK" || log "workdir kept at $WORK"
  exit $status
}
trap cleanup EXIT

require_endpoint
log "emulator: $CLOUDCC_EMULATOR_ENDPOINT"

log "building cloudcc"
( cd "$REPO_ROOT" && go build -o "$WORK/cloudcc" ./cmd/cloudcc )
cloudcc_bin="$WORK/cloudcc"

mkdir -p "$SRC"
cp -R "$REPO_ROOT/examples/$EXAMPLE/." "$SRC/"

# --------------------------------------------------- preflight refusals

log "checking that deploying without a compile is refused"
if "$cloudcc_bin" deploy "$SRC" -o "$WORK/never-compiled" --stack local --preview >/dev/null 2>&1; then
  fail "deploying a directory that was never compiled should be refused"
fi
pass "an uncompiled output is refused"

log "compiling"
"$cloudcc_bin" "$SRC" -o "$OUT" >/dev/null
APP_OUT="$(app_out "$OUT" "$SRC")"

log "checking that stale output is refused"
printf '\n\n@app.get("/added-after-compiling")\ndef added():\n    return {}\n' >> "$SRC/app.py"
if "$cloudcc_bin" deploy "$SRC" -o "$OUT" --stack local --preview >/dev/null 2>&1; then
  fail "deploying output that no longer matches the source should be refused (D19)"
fi
refusal="$("$cloudcc_bin" deploy "$SRC" -o "$OUT" --stack local --preview 2>&1 || true)"
case "$refusal" in
  *stale*) ;;
  *) fail "the refusal should say the output is stale, got: $refusal" ;;
esac
pass "stale output is refused, with an explanation"

log "checking that --force overrides the refusal"
"$cloudcc_bin" deploy "$SRC" -o "$OUT" --stack local --preview --force >/dev/null 2>&1 \
  || fail "--force should allow deploying stale output"
pass "--force overrides the refusal"

log "recompiling so the output matches again"
"$cloudcc_bin" "$SRC" -o "$OUT" >/dev/null
"$cloudcc_bin" deploy "$SRC" -o "$OUT" --stack local --preview >/dev/null \
  || fail "a freshly compiled output should preview cleanly"
pass "current output is accepted"

# ------------------------------------------------------------- deploy

log "cloudcc deploy --stack local"
"$cloudcc_bin" deploy "$SRC" -o "$OUT" --stack local
pass "deployed"

if skip_unless_service dynamodb; then
  aws_local dynamodb list-tables | grep -q petsByOwner || fail "the table was not provisioned"
  pass "the table exists"
fi

log "checking the exported bindings"
eval "$(cd "$APP_OUT" && PULUMI_BACKEND_URL="file://$APP_OUT/.pulumi-state" PULUMI_CONFIG_PASSPHRASE=cloudcc-emulator \
        pulumi stack output --json --stack local \
        | jq -r 'to_entries[] | select(.key | startswith("CLOUDCC_")) | "export \(.key)=\(.value|@sh)"')"
[ -n "${CLOUDCC_KV_PETSBYOWNER_TABLE:-}" ] || fail "the stack did not export CLOUDCC_KV_PETSBYOWNER_TABLE"
pass "bindings exported as $CLOUDCC_KV_PETSBYOWNER_TABLE"

# ------------------------------------------------------------ destroy

log "cloudcc deploy --destroy"
"$cloudcc_bin" deploy "$SRC" -o "$OUT" --stack local --destroy
DESTROYED=1

if skip_unless_service dynamodb; then
  if aws_local dynamodb list-tables | grep -q petsByOwner; then
    fail "the table survived destroy"
  fi
  pass "destroy removed the table"
fi

log "cloudcc deploy is green end to end"
