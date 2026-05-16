#!/usr/bin/env bash
set -euo pipefail
launchctl bootout system/org.nut.usbhid-ups-apc 2>/dev/null || true
launchctl bootstrap system /Library/LaunchDaemons/org.nut.usbhid-ups-apc.plist
echo "Driver restarted. Waiting 5s..."
sleep 5
upsc apc@localhost 2>&1 | grep -E "ups.status|battery.charge|Error"
