#!/usr/bin/env bash
# Tests for deploy.sh's argument handling and --install-unit, with ssh, scp
# and go stubbed out.  Nothing is built or contacted.
#
#   bash scripts/test/deploy_test.sh
set -euo pipefail

HERE=$(cd "$(dirname "$0")" && pwd)
REPO=$(cd "$HERE/../.." && pwd)

PASS=0
FAIL=0
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

# Each stub writes its arguments one per line, then a separator, to calls;
# STUB_SCP_FAIL and STUB_SSH_EXIT make scp fail or ssh exit with that code.
mkdir -p "$WORK/bin"
for tool in ssh scp go; do
  cat > "$WORK/bin/$tool" <<EOF
#!/usr/bin/env bash
{ echo "== $tool"; printf '%s\n' "\$@"; } >> "$WORK/calls"
[[ $tool != scp || -z "\${STUB_SCP_FAIL:-}" ]] || exit 1
[[ $tool != ssh ]] || exit "\${STUB_SSH_EXIT:-0}"
EOF
done
chmod +x "$WORK/bin/"*

# run executes deploy.sh from the repo root; sets OUT, CODE and CALLS.
run() {
  : > "$WORK/calls"
  set +e
  OUT=$(cd "$REPO" && PATH="$WORK/bin:$PATH" bash ./deploy.sh "$@" 2>&1 < /dev/null)
  CODE=$?
  set -e
  CALLS=$(cat "$WORK/calls")
}

check() {
  local name=$1 cond=$2
  if eval "$cond"; then
    PASS=$((PASS + 1))
  else
    FAIL=$((FAIL + 1))
    echo "FAIL: $name"
    echo "      condition: $cond"
    echo "      exit: $CODE"
    echo "$OUT" | sed 's/^/      | /'
    echo "$CALLS" | sed 's/^/      calls| /'
  fi
}

# The remote command must reach ssh as one argument, so ~ is expanded on the
# host: unquoted, the local shell turned it into this machine's home.
SCP_CALL=$'== scp\n-q\nups-mqtt.service\nscripts/install-unit.sh\npi@host:~/ups-mqtt/'
SSH_CALL=$'== ssh\n-t\npi@host\nsudo bash ~/ups-mqtt/install-unit.sh'

# ── --install-unit: ship, then run the installer with a terminal ───────────
run --install-unit pi@host
check "install-unit: exit 0"            '[[ $CODE == 0 ]]'
check "install-unit: ships, then ssh -t" '[[ $CALLS == "$SCP_CALL"$'"'"'\n'"'"'"$SSH_CALL" ]]'
check "install-unit: builds nothing"    '[[ $CALLS != *"== go"* ]]'

run pi@host --install-unit
check "install-unit: host first too"    '[[ $CODE == 0 && $CALLS == "$SCP_CALL"$'"'"'\n'"'"'"$SSH_CALL" ]]'

run --install-unit
check "install-unit: default host"      '[[ $CODE == 0 && $CALLS == *$'"'"'-t\nsweeney@garibaldi\n'"'"'* ]]'

STUB_SSH_EXIT=1 run --install-unit pi@host
check "install-unit: passes on an abort" '[[ $CODE == 1 ]]'

STUB_SCP_FAIL=1 run --install-unit pi@host
check "install-unit: scp fails, no ssh" '[[ $CODE != 0 && $CALLS != *"== ssh"* ]]'

# ── arguments ──────────────────────────────────────────────────────────────
run --frobnicate
check "unknown arg: exit 2, no calls"   '[[ $CODE == 2 && $OUT == *"unknown argument"* && -z $CALLS ]]'
run a@host b@host
check "two hosts: exit 2, no calls"     '[[ $CODE == 2 && $OUT == *"more than one host"* && -z $CALLS ]]'

echo "deploy: $PASS passed, $FAIL failed"
[[ $FAIL == 0 ]]
