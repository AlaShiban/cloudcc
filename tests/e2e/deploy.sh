#!/usr/bin/env bash
# End-to-end test of `cc deploy` itself.
#
# tests/e2e/ministack.sh drives Pulumi directly, which is what proves the
# generated project is sound. This one goes through cc's own deploy command,
# which is what proves the preflight, the emulator stack configuration and the
# packaging sequence work.
set -euo pipefail

source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

EXAMPLE="${1:-petstore}"
WORK="${CC_E2E_WORKDIR:-$(mktemp -d "${TMPDIR:-/tmp}/cc-deploy-XXXXXX")}"
SRC="$WORK/src"
OUT="$WORK/compiled"
KEEP="${CC_E2E_KEEP:-0}"

cleanup() {
  local status=$?
  if [ -d "$OUT" ] && [ "${DESTROYED:-0}" != "1" ]; then
    "$WORK/cc" deploy "$SRC" -o "$OUT" --stack ministack --destroy >/dev/null 2>&1 || true
  fi
  [ "$KEEP" = "0" ] && rm -rf "$WORK" || log "workdir kept at $WORK"
  exit $status
}
trap cleanup EXIT

require_endpoint
log "emulator: $MINISTACK_ENDPOINT"

log "building cc"
( cd "$REPO_ROOT" && go build -o "$WORK/cc" ./cmd/cc )
cc_bin="$WORK/cc"

mkdir -p "$SRC"
cp -R "$REPO_ROOT/examples/$EXAMPLE/." "$SRC/"

# --------------------------------------------------- preflight refusals

log "checking that deploying without a compile is refused"
if "$cc_bin" deploy "$SRC" -o "$WORK/never-compiled" --stack ministack --preview >/dev/null 2>&1; then
  fail "deploying a directory that was never compiled should be refused"
fi
pass "an uncompiled output is refused"

log "compiling"
"$cc_bin" "$SRC" -o "$OUT" >/dev/null

log "checking that stale output is refused"
printf '\n\n@app.get("/added-after-compiling")\ndef added():\n    return {}\n' >> "$SRC/app.py"
if "$cc_bin" deploy "$SRC" -o "$OUT" --stack ministack --preview >/dev/null 2>&1; then
  fail "deploying output that no longer matches the source should be refused (D19)"
fi
refusal="$("$cc_bin" deploy "$SRC" -o "$OUT" --stack ministack --preview 2>&1 || true)"
case "$refusal" in
  *stale*) ;;
  *) fail "the refusal should say the output is stale, got: $refusal" ;;
esac
pass "stale output is refused, with an explanation"

log "checking that --force overrides the refusal"
"$cc_bin" deploy "$SRC" -o "$OUT" --stack ministack --preview --force >/dev/null 2>&1 \
  || fail "--force should allow deploying stale output"
pass "--force overrides the refusal"

log "recompiling so the output matches again"
"$cc_bin" "$SRC" -o "$OUT" >/dev/null
"$cc_bin" deploy "$SRC" -o "$OUT" --stack ministack --preview >/dev/null \
  || fail "a freshly compiled output should preview cleanly"
pass "current output is accepted"

# ------------------------------------------------------------- deploy

log "cc deploy --stack ministack"
"$cc_bin" deploy "$SRC" -o "$OUT" --stack ministack
pass "deployed"

if skip_unless_service dynamodb; then
  aws_local dynamodb list-tables | grep -q petsByOwner || fail "the table was not provisioned"
  pass "the table exists"
fi

log "checking the exported bindings"
eval "$(cd "$OUT" && PULUMI_BACKEND_URL="file://$OUT/.pulumi-state" PULUMI_CONFIG_PASSPHRASE=cc-emulator \
        pulumi stack output --json --stack ministack \
        | jq -r 'to_entries[] | select(.key | startswith("CC_")) | "export \(.key)=\(.value|@sh)"')"
[ -n "${CC_KV_PETSBYOWNER_TABLE:-}" ] || fail "the stack did not export CC_KV_PETSBYOWNER_TABLE"
pass "bindings exported as $CC_KV_PETSBYOWNER_TABLE"

# ------------------------------------------------------------ destroy

log "cc deploy --destroy"
"$cc_bin" deploy "$SRC" -o "$OUT" --stack ministack --destroy
DESTROYED=1

if skip_unless_service dynamodb; then
  if aws_local dynamodb list-tables | grep -q petsByOwner; then
    fail "the table survived destroy"
  fi
  pass "destroy removed the table"
fi

log "cc deploy is green end to end"
