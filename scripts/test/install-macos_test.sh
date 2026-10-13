#!/usr/bin/env bash
# Tests for install-macos.sh's LaunchAgent plist: that the template renders
# to a valid plist carrying STATE_DIRECTORY, and that an invalid one stops
# the install before anything is built or installed.  The "home" is a temp
# directory; go is stubbed.  Needs plutil, so it is skipped off macOS.
#
#   bash scripts/test/install-macos_test.sh
set -euo pipefail

HERE=$(cd "$(dirname "$0")" && pwd)
REPO="$HERE/../.."

if ! command -v plutil > /dev/null; then
  echo "install-macos: skipped (no plutil; macOS only)"
  exit 0
fi

PASS=0
FAIL=0
WORK=$(mktemp -d "${TMPDIR:-/tmp}/install-macos-test.XXXXXX")
trap 'rm -rf "$WORK"' EXIT

check() {
  if eval "$2"; then PASS=$((PASS + 1)); else FAIL=$((FAIL + 1)); echo "FAIL: $1"; fi
}

# ── the template renders to a valid plist, a space in the home included ────
FAKEHOME="$WORK/home with space"
mkdir -p "$FAKEHOME"
sed "s|HOME_DIR|${FAKEHOME}|g" "$REPO/net.swee.ups-mqtt.plist" > "$WORK/rendered.plist"
check "template: lints"                'plutil -lint -s "$WORK/rendered.plist"'
STATE=$(plutil -extract EnvironmentVariables.STATE_DIRECTORY raw -o - "$WORK/rendered.plist")
check "template: STATE_DIRECTORY"      '[[ $STATE == "$FAKEHOME/Library/Application Support/ups-mqtt" ]]'
check "template: no HOME_DIR left"     '! grep -q HOME_DIR "$WORK/rendered.plist"'

# ── an invalid plist stops the install before anything changes ────────────
CO="$WORK/checkout"
mkdir -p "$CO" "$WORK/bin"
cp "$REPO/install-macos.sh" "$CO/"
sed 's|<dict>|<dict><key>broken|' "$REPO/net.swee.ups-mqtt.plist" > "$CO/net.swee.ups-mqtt.plist"
printf '#!/usr/bin/env bash\necho "go $*" >> "%s/go-calls"\n' "$WORK" > "$WORK/bin/go"
chmod +x "$WORK/bin/go"
set +e
OUT=$(cd "$CO" && HOME="$FAKEHOME" PATH="$WORK/bin:$PATH" bash ./install-macos.sh 2>&1 < /dev/null)
CODE=$?
set -e
check "invalid plist: refused"         '[[ $CODE == 1 && $OUT == *"rendered plist is invalid; nothing changed"* ]]'
check "  …nothing built"               '[[ ! -e $WORK/go-calls ]]'
check "  …nothing installed"           '[[ -z "$(ls -A "$FAKEHOME")" ]]'
check "  …temp plist removed"          '! ls /tmp/net.swee.ups-mqtt.*.plist 2>/dev/null | xargs -r grep -l "<key>broken" 2>/dev/null | grep -q .'

echo "install-macos: $PASS passed, $FAIL failed"
[[ $FAIL -eq 0 ]]
