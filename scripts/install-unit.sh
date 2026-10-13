#!/usr/bin/env bash
# install-unit.sh — bring the installed ups-mqtt systemd unit up to date.
#
# deploy.sh replaces the binary but not the unit: the deploy user's sudoers
# entry is deliberately narrow, so a change to ups-mqtt.service (ordering,
# StateDirectory=, …) would never reach a host that is already installed.
# deploy.sh ships this script next to the unit file and runs --check, which
# says when it is needed.
#
# It renders the repo's unit with the User= the installed unit already has,
# shows the diff, checks it with systemd-analyze, and installs it after a
# prompt. Then it runs daemon-reload and restarts ups-mqtt if it is running,
# so the new unit takes effect.
#
# Usage (on the host):
#   ./install-unit.sh --check        # show the diff, change nothing, no root needed
#   sudo ./install-unit.sh           # install, after a confirmation prompt
#   sudo ./install-unit.sh --yes     # install without prompting
#   ./install-unit.sh ... UNIT_FILE  # default: ups-mqtt.service next to this
#                                    # script, else in its parent directory
#
# Exit codes: 0 up to date (or installed), 1 changes needed (--check) or
# aborted at the prompt, 2 error.
#
# INSTALL_UNIT_ROOT prefixes the install path, so tests can run against a
# fixture tree.  See scripts/test/install-unit_test.sh.
set -euo pipefail

SERVICE="ups-mqtt"
ROOT="${INSTALL_UNIT_ROOT:-}"
INSTALLED="${ROOT}/etc/systemd/system/${SERVICE}.service"
PLACEHOLDER="User=SERVICE_USER"

die() { echo "ERROR: $*" >&2; exit 2; }
say() { echo "==> $*"; }

usage() { sed -n '2,/^set -euo/p' "$0" | grep '^#' | sed 's/^# \{0,1\}//'; }

CHECK=0
YES=0
SOURCE=""
for arg in "$@"; do
  case "$arg" in
    --check|--dry-run) CHECK=1 ;;
    --yes|-y)          YES=1 ;;
    -h|--help)         usage; exit 0 ;;
    -*)                usage >&2; die "unknown argument: $arg" ;;
    *)                 [[ -z "$SOURCE" ]] || die "more than one unit file given"
                       SOURCE="$arg" ;;
  esac
done

if [[ -z "$SOURCE" ]]; then
  here=$(cd "$(dirname "$0")" && pwd)
  for candidate in "$here/${SERVICE}.service" "$here/../${SERVICE}.service"; do
    if [[ -f "$candidate" ]]; then SOURCE="$candidate"; break; fi
  done
  [[ -n "$SOURCE" ]] || die "no ${SERVICE}.service next to this script or in its parent; pass the unit file"
fi

# ── Preconditions ──────────────────────────────────────────────────────────
[[ -f "$SOURCE" ]] || die "unit file not found: $SOURCE"
grep -qx "$PLACEHOLDER" "$SOURCE" || die "$SOURCE has no '$PLACEHOLDER' line to fill in"
[[ -f "$INSTALLED" ]] || die "no unit installed at ${INSTALLED#"$ROOT"} — this updates an existing install; run first-install.sh for a new host"

# Keep whoever the service already runs as.  first-install.sh chose them.
user=$(sed -n 's/^User=//p' "$INSTALLED" | tail -1)
[[ -n "$user" ]] || die "${INSTALLED#"$ROOT"} has no User= line; refusing to guess who ${SERVICE} runs as"
[[ "$user" != "SERVICE_USER" ]] || die "${INSTALLED#"$ROOT"} still has the User= placeholder; fix it by hand"

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
# systemd-analyze judges a unit by its file name, so keep the real one.
RENDERED="$WORK/${SERVICE}.service"
sed "s/^${PLACEHOLDER}\$/User=${user}/" "$SOURCE" > "$RENDERED"

# ── Compare ────────────────────────────────────────────────────────────────
say "installed: ${INSTALLED#"$ROOT"} (User=${user})"
say "source:    ${SOURCE}"
if cmp -s "$INSTALLED" "$RENDERED"; then
  say "up to date — nothing to do"
  exit 0
fi

echo
diff -u --label "installed ${INSTALLED#"$ROOT"}" --label "new" "$INSTALLED" "$RENDERED" || true
echo

if command -v systemd-analyze >/dev/null 2>&1; then
  if ! out=$(systemd-analyze verify "$RENDERED" 2>&1); then
    echo "$out" >&2
    die "systemd-analyze rejects the new unit; nothing changed"
  fi
  say "systemd-analyze verify: ok"
else
  say "systemd-analyze not found; skipping verification"
fi

if [[ $CHECK == 1 ]]; then
  echo "Would: install the unit above, run daemon-reload, and restart ${SERVICE} if it is running"
  exit 1
fi

# ── Apply ──────────────────────────────────────────────────────────────────
if [[ -z "$ROOT" && $EUID -ne 0 ]]; then
  die "installing needs root: run with sudo (or use --check)"
fi

echo "About to:"
echo "  - replace ${INSTALLED#"$ROOT"} with the diff above"
echo "  - run systemctl daemon-reload"
echo "  - restart ${SERVICE} if it is running (a few seconds without polling)"
if [[ $YES == 0 ]]; then
  read -r -p "Continue? [y/N] " answer || answer=""
  case "$answer" in
    y|Y|yes|YES) ;;
    *) echo "Aborted; nothing changed."; exit 1 ;;
  esac
fi

# Write beside the target and rename, so systemd never sees a half-written
# unit.  An installed symlink is replaced by a file, leaving its target alone.
cp "$RENDERED" "${INSTALLED}.tmp"
chmod 644 "${INSTALLED}.tmp"
mv -f "${INSTALLED}.tmp" "$INSTALLED"
say "installed ${INSTALLED#"$ROOT"}"

systemctl daemon-reload || die "systemctl daemon-reload failed; the new unit is installed but not loaded"
systemctl try-restart "$SERVICE" || die "systemctl try-restart ${SERVICE} failed; check: systemctl status ${SERVICE}"
say "done: daemon reloaded; ${SERVICE} restarted if it was running"
