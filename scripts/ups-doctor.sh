#!/usr/bin/env bash
#
# ups-doctor.sh — diagnose and, where possible, repair the NUT → MQTT chain
# on a macOS host.
#
# The chain has four links, and each fails differently:
#
#   UPS ──USB──> usbhid-ups driver ──socket──> upsd ──TCP:3493──> ups-mqtt
#
# "Error: Data stale" or "Driver not connected" can mean any of them, so this
# script identifies WHICH link is broken before touching anything. Some faults
# are repairable in software; the ones that need hands are reported clearly
# rather than worked around.
#
# Usage:
#   sudo ./scripts/ups-doctor.sh            # diagnose, then repair what is safe
#   sudo ./scripts/ups-doctor.sh diagnose   # report only, change nothing
#
# Exit codes:
#   0  healthy (possibly after repair)
#   1  needs physical intervention (replug / power-cycle)
#   2  error
#
set -euo pipefail

UPS="cyberpower"
DRIVER_LABEL="org.nut.usbhid-ups-cyberpower"
DRIVER_PLIST="/Library/LaunchDaemons/${DRIVER_LABEL}.plist"
UPSD_LABEL="org.nut.upsd"
UPSD_PLIST="/Library/LaunchDaemons/${UPSD_LABEL}.plist"
MQTT_LABEL="net.swee.ups-mqtt"
USB_VENDOR_DEC=1892            # CyberPower 0x0764, as ioreg reports it
NUT_BIN_DIR="/opt/homebrew/bin"
DRIVER_LOG="/opt/homebrew/var/log/nut-driver.log"
SETTLE=6                       # seconds to let a driver claim the device

MODE="${1:-repair}"
DRY=0; [ "$MODE" = "diagnose" ] && DRY=1

say()  { printf '%s\n' "$*"; }
step() { printf '\n== %s ==\n' "$*"; }
ok()   { printf '   OK    %s\n' "$*"; }
bad()  { printf '   FAIL  %s\n' "$*"; }
note() { printf '         %s\n' "$*"; }

[ "$(id -u)" -eq 0 ] || { say "must run as root: sudo $0 ${MODE}"; exit 2; }

# Without this, `set -e` aborts mid-run leaving no clue which check died.
trap 'rc=$?; [ "$rc" -gt 2 ] && printf "\n!! aborted unexpectedly (exit %s) near line %s\n" "$rc" "$LINENO"; exit "$rc"' ERR

# The GUI LaunchAgent lives in the invoking user's domain, not root's.
GUI_USER="${SUDO_USER:-}"
GUI_UID=""
[ -n "$GUI_USER" ] && GUI_UID=$(id -u "$GUI_USER" 2>/dev/null || true)

# ---------------------------------------------------------------------------
# 1. USB layer — is the device on the bus, and does it answer descriptors?
# ---------------------------------------------------------------------------
step "USB device"
on_bus=$(ioreg -p IOUSB -w0 -l 2>/dev/null | grep -c "\"idVendor\" = ${USB_VENDOR_DEC}" || true)

if [ "$on_bus" -eq 0 ]; then
    bad "UPS is not on the USB bus at all"
    note "Nothing in software can fix this. Reseat the USB lead — preferably"
    note "into a port directly on the machine rather than through a hub — then"
    note "re-run this script."
    exit 1
fi
ok "UPS present on the USB bus"

# A wedged device enumerates but will not serve its string descriptors, so the
# driver never reaches the matching stage. Probing with the real driver is the
# only reliable way to tell: it exercises the same libusb path.
probe=$("${NUT_BIN_DIR}/usbhid-ups" -a "${UPS}" -u root -DD -d1 2>&1 || true)
if printf '%s' "$probe" | grep -q "Product: unknown"; then
    bad "device enumerates but will not answer descriptor reads (Product: unknown)"
    note "Its USB interface has hung. A host reboot will NOT clear this — the"
    note "wedge is in the device. Unplug the USB lead, wait ~10s, replug, then"
    note "re-run. If that fails, the UPS itself needs power-cycling."
    exit 1
fi
ok "device answers descriptor reads"

# ---------------------------------------------------------------------------
# 2. Driver layer
# ---------------------------------------------------------------------------
step "NUT driver (${DRIVER_LABEL})"
driver_running() { pgrep -f "usbhid-ups -a ${UPS}" >/dev/null 2>&1; }

restart_driver() {
    # Never kill the process directly: launchd would respawn it before macOS
    # released the USB device, and it would fail with "No matching HID UPS
    # found". Boot out, wait for the process to actually exit, then bootstrap.
    say "   restarting driver..."
    launchctl bootout "system/${DRIVER_LABEL}" 2>/dev/null || true
    for _ in $(seq 1 15); do driver_running || break; sleep 1; done
    if driver_running; then
        bad "old driver process would not exit; not bootstrapping (would race)"
        return 1
    fi
    # Bootstrap can transiently fail with EIO(5) if launchd is still tearing
    # the job down. Retry rather than leaving the driver stopped.
    local i
    for i in 1 2 3; do
        if launchctl bootstrap system "${DRIVER_PLIST}" 2>/dev/null; then return 0; fi
        sleep 2
    done
    bad "bootstrap failed 3x — driver is DOWN"
    note "recover with: launchctl bootstrap system ${DRIVER_PLIST}"
    return 1
}

if driver_running; then
    ok "driver process running"
else
    bad "driver not running"
    [ "$DRY" -eq 1 ] || restart_driver || exit 2
fi

# ---------------------------------------------------------------------------
# 3. upsd layer — is the data actually fresh?
# ---------------------------------------------------------------------------
step "upsd data"
# `upsc` exits non-zero whenever it reports an error ("Data stale", "Driver not
# connected"), which is precisely the case this script exists to handle. With
# `set -o pipefail` that would abort the run, so the status is captured
# deliberately rather than allowed to propagate.
ups_state() { "${NUT_BIN_DIR}/upsc" "${UPS}@localhost" ups.status 2>&1 | head -1 || true; }

state=$(ups_state)
if printf '%s' "$state" | grep -qE "^(OL|OB|LB|CHRG|DISCHRG)"; then
    ok "data fresh — ups.status = ${state}"
else
    bad "upsd says: ${state}"
    if [ "$DRY" -eq 0 ]; then
        # Stale data with a healthy device usually means the driver lost the
        # device mid-flight; a restart reclaims it.
        restart_driver || exit 2
        sleep "$SETTLE"
        state=$(ups_state)
        if ! printf '%s' "$state" | grep -qE "^(OL|OB|LB|CHRG|DISCHRG)"; then
            # Driver is fine but upsd may be holding a dead handle to it.
            say "   still stale; restarting upsd..."
            launchctl bootout "system/${UPSD_LABEL}" 2>/dev/null || true
            sleep 2
            launchctl bootstrap system "${UPSD_PLIST}" 2>/dev/null || true
            sleep "$SETTLE"
            state=$(ups_state)
        fi
        if printf '%s' "$state" | grep -qE "^(OL|OB|LB|CHRG|DISCHRG)"; then
            ok "recovered — ups.status = ${state}"
        else
            bad "still not reporting: ${state}"
            note "last driver log lines:"
            tail -5 "${DRIVER_LOG}" 2>/dev/null | sed 's/^/         /'
            exit 1
        fi
    fi
fi

# ---------------------------------------------------------------------------
# 4. ups-mqtt — recovers by itself; only report, do not restart
# ---------------------------------------------------------------------------
# Poll closes and re-dials a dead connection inside a single read, so the
# daemon picks NUT back up on its own at its next read (every sample_interval).
# Restarting it would publish online:false and briefly mark the device offline
# to subscribers, for no gain. (Before 2026-09-15 this was not true — the go.nut-based client could
# not close a socket and a repair did need a manual kick.)
step "ups-mqtt"
if [ -z "$GUI_UID" ]; then
    note "cannot reach the user LaunchAgent domain (no SUDO_USER); not checked."
elif launchctl print "gui/${GUI_UID}/${MQTT_LABEL}" >/dev/null 2>&1; then
    ok "running — it picks upsd back up at its next read (every sample_interval)"
else
    bad "${MQTT_LABEL} is not loaded"
    note "start it from a user shell (not via sudo):"
    note "  launchctl kickstart -k gui/\$(id -u)/${MQTT_LABEL}"
fi

step "Result"
say "ups.status = $(ups_state)"
say "Verify publishing with:  mosquitto_sub -h <broker> -t 'ups/+/state' -v -C 1"
