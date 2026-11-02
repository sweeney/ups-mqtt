#!/usr/bin/env bash
# Tests for ups-doctor.sh.  It needs root and real hardware, so these run it
# with stand-ins for `id`, `ioreg` and `log` on PATH, and cover the part that
# decides what to tell someone whose UPS has left the USB bus: that branch
# reads, reports and exits, changing nothing.  DISABLED and BACK are real
# kernel log lines from 2026-11-02 and 2026-11-03 (docs/live-tests.md);
# DETACHED is made up, a departure for any other reason.
#
#   bash scripts/test/ups-doctor_test.sh
set -euo pipefail

HERE=$(cd "$(dirname "$0")" && pwd)
SCRIPT="$HERE/../ups-doctor.sh"

PASS=0
FAIL=0
check() {
  if eval "$2"; then PASS=$((PASS + 1)); else FAIL=$((FAIL + 1)); echo "FAIL: $1"; echo "$OUT" | sed 's/^/    /'; fi
}

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
BIN="$TMP/bin"
mkdir -p "$BIN"

# id -u answers $FAKE_UID; ioreg prints $FAKE_IOREG; log prints $FAKE_LOG.
cat > "$BIN/id" <<'EOF'
#!/usr/bin/env bash
[ "${1:-}" = "-u" ] && [ $# -eq 1 ] && { echo "${FAKE_UID:-0}"; exit 0; }
exit 1
EOF
cat > "$BIN/ioreg" <<'EOF'
#!/usr/bin/env bash
cat "$FAKE_IOREG"
EOF
cat > "$BIN/log" <<'EOF'
#!/usr/bin/env bash
[ "${1:-}" = "show" ] || exit 64
cat "$FAKE_LOG"
EOF
chmod +x "$BIN"/*

: > "$TMP/ioreg-empty"
printf '    "idVendor" = 1452\n' > "$TMP/ioreg-other"

HDR='Timestamp               Ty Process[PID:TID]'
DISABLED='2026-11-02 03:51:20.848 Df kernel[0:5c1] (IOUSBHostFamily) usb-drd0-port-hs@00100000: AppleUSBHostPort::terminateDevice: destroying 0x0764/0501/0001 (CP1500EPFCLCD): hardware port disabled'
BACK='2026-11-03 09:56:12.350 Df kernel[0:2a4d278] (IOUSBHostFamily) usb-drd1-port-hs@01100000: AppleUSBHostPort::enumerateDeviceComplete_block_invoke: enumerated 0x0764/0501/0001 (CP1500EPFCLCD / 2) at 12 Mbps'
DETACHED='2026-06-25 06:29:02.112 Df kernel[0:5c1] (IOUSBHostFamily) usb-drd0-port-hs@00100000: AppleUSBHostPort::terminateDevice: destroying 0x0764/0501/0001 (CP1500EPFCLCD): device detached'

printf '%s\n' "$HDR" > "$TMP/log-none"
printf '%s\n' "$HDR" "$BACK" "$DISABLED" > "$TMP/log-disabled"
printf '%s\n' "$HDR" "$DISABLED" "$BACK" > "$TMP/log-arrived"
printf '%s\n' "$HDR" "$DETACHED" > "$TMP/log-detached"

# run <uid> <ioreg fixture> <log fixture> [mode] — sets OUT and RC.
run() {
  set +e
  OUT=$(PATH="$BIN:$PATH" FAKE_UID="$1" FAKE_IOREG="$TMP/$2" FAKE_LOG="$TMP/$3" \
        bash "$SCRIPT" ${4:+"$4"} 2>&1)
  RC=$?
  set -e
}

check "parses" 'bash -n "$SCRIPT"'

run 501 ioreg-empty log-none diagnose
check "refuses without root (exit 2)"           '[ "$RC" -eq 2 ] && grep -q "must run as root" <<<"$OUT"'

run 0 ioreg-empty log-disabled diagnose
check "port disabled: needs hands (exit 1)"      '[ "$RC" -eq 1 ]'
check "port disabled: names the cause and time"  'grep -q "macOS disabled its USB port at 2026-11-02 03:51:20" <<<"$OUT"'
check "port disabled: quotes the kernel line"    'grep -q "destroying 0x0764/0501/0001 (CP1500EPFCLCD): hardware port disabled" <<<"$OUT"'
check "port disabled: says replug, no reboot"    'grep -q "replug" <<<"$OUT" && grep -q "No reboot needed" <<<"$OUT"'

run 0 ioreg-empty log-disabled
check "repair mode gives the same advice"        '[ "$RC" -eq 1 ] && grep -q "macOS disabled its USB port" <<<"$OUT"'

run 0 ioreg-other log-disabled diagnose
check "another vendor on the bus is not the UPS" '[ "$RC" -eq 1 ] && grep -q "not on the USB bus" <<<"$OUT"'

run 0 ioreg-empty log-arrived diagnose
check "re-enumerated after a disable: not that"  '! grep -q "macOS disabled its USB port" <<<"$OUT"'
check "arrival but no departure is said"         'grep -q "has its arrival at 2026-11-03 09:56:12 but not its departure" <<<"$OUT"'

run 0 ioreg-empty log-detached diagnose
check "other departure: reports the reason"      'grep -q "left the bus at 2026-06-25 06:29:02" <<<"$OUT" && grep -q "device detached" <<<"$OUT"'
check "other departure: reseat, then reboot"     'grep -q "Reseat" <<<"$OUT" && grep -q "only a reboot clears it" <<<"$OUT"'

run 0 ioreg-empty log-none diagnose
check "no record: says the log has aged out"     'grep -q "no longer says why it left" <<<"$OUT"'
check "no record: replug first, reboot after"    'grep -q "Reseat the USB lead first" <<<"$OUT" && grep -q "only a reboot clears it" <<<"$OUT"'

echo "ups-doctor: $PASS passed, $FAIL failed"
[[ $FAIL == 0 ]]
