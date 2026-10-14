#!/usr/bin/env bash
# Tests for recover-ups-usb.sh.  It needs root and real hardware (pmset,
# ioreg), so this only checks what can be checked without them: it parses,
# and it never restarts ups-mqtt, which re-dials NUT by itself — a restart
# would only publish online:false for nothing (scripts/README.md).
#
#   bash scripts/test/recover-ups-usb_test.sh
set -euo pipefail

HERE=$(cd "$(dirname "$0")" && pwd)
SCRIPT="$HERE/../recover-ups-usb.sh"

PASS=0
FAIL=0
check() {
  if eval "$2"; then PASS=$((PASS + 1)); else FAIL=$((FAIL + 1)); echo "FAIL: $1"; fi
}

check "parses"                         'bash -n "$SCRIPT"'
check "never restarts ups-mqtt"        '! grep -nE "net\.swee\.ups-mqtt|systemctl (restart|start) ups-mqtt" "$SCRIPT"'
check "still kicks the NUT driver"     'grep -q "kickstart -k \"system/\${DRIVER_LABEL}\"" "$SCRIPT"'

echo "recover-ups-usb: $PASS passed, $FAIL failed"
[[ $FAIL == 0 ]]
