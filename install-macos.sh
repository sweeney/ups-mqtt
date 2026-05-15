#!/usr/bin/env bash
# install-macos.sh — build and install ups-mqtt as a LaunchAgent on macOS.
#
# Usage:
#   ./install-macos.sh          # install / update
#
# Re-running this script is safe: it replaces the binary and reloads the
# service.  Config is never overwritten if it already exists.
#
# The binary is installed inside a minimal .app bundle so macOS can track
# and prompt for the Local Network privacy permission.
#
# Sudo is required only to create /etc/ups-mqtt/config.toml on first install.
#
# Prerequisites:
#   - NUT installed and configured (brew install nut; see org.nut.*.plist)
#   - config.toml (or config.toml.example) present in the repo root
set -euo pipefail

SERVICE="ups-mqtt"
BUNDLE_ID="net.swee.ups-mqtt"
PLIST_TEMPLATE="org.ups-mqtt.plist"
PLIST_LABEL="org.ups-mqtt"   # used by launchctl bootstrap/bootout
HOME_DIR="${HOME}"
AGENT_DIR="${HOME_DIR}/Library/LaunchAgents"
PLIST_DST="${AGENT_DIR}/org.ups-mqtt.plist"
APP_BUNDLE="${HOME_DIR}/Applications/ups-mqtt.app"
APP_BIN="${APP_BUNDLE}/Contents/MacOS/${SERVICE}"
CONFIG_DST="/etc/${SERVICE}/config.toml"

# ---------------------------------------------------------------------------
# 1. Detect architecture and build
# ---------------------------------------------------------------------------
case "$(uname -m)" in
    arm64)  GOARCH=arm64 ;;
    x86_64) GOARCH=amd64 ;;
    *) echo "Unsupported architecture: $(uname -m)"; exit 1 ;;
esac

echo "==> Building for darwin/${GOARCH}..."
GOOS=darwin GOARCH="${GOARCH}" go build -ldflags="-s -w" -o "${SERVICE}" ./cmd/${SERVICE}/
trap 'rm -f "./${SERVICE}"' EXIT

# ---------------------------------------------------------------------------
# 2. App bundle (no sudo — ~/Applications is user-owned)
# ---------------------------------------------------------------------------
mkdir -p "${APP_BUNDLE}/Contents/MacOS"
cp "./${SERVICE}" "${APP_BIN}"
chmod +x "${APP_BIN}"

# Write Info.plist — gives macOS a bundle identifier to track in TCC,
# enabling the Local Network permission prompt.
cat > "${APP_BUNDLE}/Contents/Info.plist" << INFOPLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleIdentifier</key>
    <string>${BUNDLE_ID}</string>
    <key>CFBundleName</key>
    <string>ups-mqtt</string>
    <key>CFBundleExecutable</key>
    <string>${SERVICE}</string>
    <key>CFBundleVersion</key>
    <string>1</string>
    <key>CFBundleShortVersionString</key>
    <string>1.0</string>
    <key>NSLocalNetworkUsageDescription</key>
    <string>ups-mqtt connects to your MQTT broker and NUT server on the local network.</string>
</dict>
</plist>
INFOPLIST

codesign --force --sign - --identifier "${BUNDLE_ID}" "${APP_BUNDLE}"
echo "    Bundle    → ${APP_BUNDLE}"

# ---------------------------------------------------------------------------
# 3. Config — only on first install (requires sudo for /etc/)
# ---------------------------------------------------------------------------
if [ ! -f "${CONFIG_DST}" ]; then
    echo "==> Creating config (you may be prompted for your sudo password)..."
    sudo mkdir -p "/etc/${SERVICE}"
    if [ -f "config.toml" ]; then
        sudo cp config.toml "${CONFIG_DST}"
        echo "    Config    → ${CONFIG_DST}"
    else
        sudo cp config.toml.example "${CONFIG_DST}"
        echo "    Config    → ${CONFIG_DST} (from example — edit before starting)"
    fi
else
    echo "    Config already exists at ${CONFIG_DST} — skipping."
fi

# ---------------------------------------------------------------------------
# 4. LaunchAgent plist — points into the app bundle
# ---------------------------------------------------------------------------
mkdir -p "${AGENT_DIR}"
PATCHED_PLIST="$(mktemp /tmp/org.ups-mqtt.XXXXXX.plist)"
trap 'rm -f "./${SERVICE}" "${PATCHED_PLIST}"' EXIT
sed "s|HOME_DIR|${HOME_DIR}|g" "${PLIST_TEMPLATE}" > "${PATCHED_PLIST}"

launchctl bootout "gui/$(id -u)/${PLIST_LABEL}" 2>/dev/null || true
cp "${PATCHED_PLIST}" "${PLIST_DST}"
launchctl bootstrap "gui/$(id -u)" "${PLIST_DST}"
echo "    Plist     → ${PLIST_DST}"
echo "    Service loaded."

# ---------------------------------------------------------------------------
# 5. Done
# ---------------------------------------------------------------------------
echo ""
echo "==> ${SERVICE} installed."
echo "    Bundle:  ${APP_BUNDLE}"
echo "    Config:  ${CONFIG_DST}"
echo "    Logs:    ${HOME_DIR}/Library/Logs/${SERVICE}.log"
echo ""
echo "  If this is the first install, run the binary once from the terminal"
echo "  to trigger the macOS Local Network permission prompt:"
echo "    ${APP_BIN} --config ${CONFIG_DST}"
echo ""
echo "    Stop:    launchctl bootout gui/\$(id -u)/${PLIST_LABEL}"
echo "    Start:   launchctl bootstrap gui/\$(id -u) ${PLIST_DST}"
echo "    Update:  ./install-macos.sh"
echo ""
