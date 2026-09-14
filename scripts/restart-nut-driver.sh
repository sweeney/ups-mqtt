#!/usr/bin/env bash
set -euo pipefail

# Clean restart of the NUT USB HID driver for the CyberPower CP1500EPFCLCD.
# Must be run as root: libusb needs root to claim the USB HID device on macOS.
# Do NOT just kill the driver process — launchd would restart it before macOS
# releases the USB device, and it would fail with "No matching HID UPS found".

LABEL="org.nut.usbhid-ups-cyberpower"
PLIST="/Library/LaunchDaemons/${LABEL}.plist"
UPS="cyberpower"

echo "Booting out ${LABEL} ..."
launchctl bootout "system/${LABEL}" 2>/dev/null || true

# Wait for the old driver process to actually exit and release the USB device
# before we bootstrap again — otherwise bootstrap races and fails with EIO (5).
for i in $(seq 1 10); do
  pgrep -f "usbhid-ups -a ${UPS}" >/dev/null || break
  sleep 1
done

echo "Bootstrapping ${LABEL} ..."
launchctl bootstrap system "${PLIST}"

echo "Driver restarted. Waiting 5s..."
sleep 5
upsc "${UPS}@localhost" 2>&1 | grep -E "ups.status|battery.charge|Error"
