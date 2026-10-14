#!/usr/bin/env bash
# deploy.sh — build, ship, and restart ups-mqtt on a remote host.
#
# Usage:
#   ./deploy.sh --site NAME        # deploy to a site's host
#   ./deploy.sh --host USER@HOST   # deploy to a host not in the sites file
#   ./deploy.sh --install-unit --site NAME
#                                  # install this checkout's systemd unit
#                                  # (asks first, needs sudo); no build
#   ./deploy.sh --install-sudoers --site NAME
#                                  # install the passwordless-sudo rule this
#                                  # script needs (asks first, needs sudo)
#
#   --site NAME        a site in the local sites file (copy sites.example;
#                      UPS_MQTT_SITES names another file).  There is no
#                      default, so a deploy always names the host it changes.
#   --host USER@HOST   the ssh target; overrides the site's
#
# Prerequisites on the target host (one-time, run first-install.sh):
#   - /etc/ups-mqtt/config.toml exists
#   - /usr/local/bin/ups-mqtt symlink exists
#   - /etc/systemd/system/ups-mqtt.service exists
#   - passwordless sudo for exactly what this script runs as root
#     (systemctl daemon-reload, systemctl restart ups-mqtt, and tee of the
#     config): scripts/install-sudoers.sh
#
# See first-install.sh for the one-time setup procedure.
set -euo pipefail
cd "$(dirname "$0")"   # every path below is relative to the checkout

INSTALL=""
SITE=""
HOST=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --install-unit)    [[ -z "$INSTALL" ]] || { echo "ERROR: one --install-* at a time" >&2; exit 2; }
                       INSTALL=install-unit.sh ;;
    --install-sudoers) [[ -z "$INSTALL" ]] || { echo "ERROR: one --install-* at a time" >&2; exit 2; }
                       INSTALL=install-sudoers.sh ;;
    --site)            [[ $# -ge 2 && -n "$2" && "$2" != -* ]] || { echo "ERROR: --site needs a name" >&2; exit 2; }
                       [[ -z "$SITE" ]] || { echo "ERROR: --site given twice" >&2; exit 2; }
                       SITE=$2; shift ;;
    --host)            [[ $# -ge 2 && -n "$2" && "$2" != -* ]] || { echo "ERROR: --host needs USER@HOST" >&2; exit 2; }
                       [[ -z "$HOST" ]] || { echo "ERROR: --host given twice" >&2; exit 2; }
                       HOST=$2; shift ;;
    -h|--help)         sed -n '2,/^set -euo/p' "$(basename "$0")" | grep '^#' | sed 's/^# \{0,1\}//'; exit 0 ;;
    -*)                echo "ERROR: unknown argument: $1" >&2; exit 2 ;;
    *)                 echo "ERROR: name the host with --site NAME or --host USER@HOST, not '$1'" >&2; exit 2 ;;
  esac
  shift
done
# shellcheck source=deploy-sites.sh
source ./deploy-sites.sh
TARGET=$(resolve_target "$SITE" "$HOST") || exit 2
# How to name this host again in the hints printed below.
if [[ -n "$HOST" ]]; then WHERE="--host ${HOST}"; else WHERE="--site ${SITE}"; fi
SERVICE="ups-mqtt"

# The unit and the root-only installers travel with every deploy; installing
# needs root, which this script deliberately does not have unattended.  They
# go to ~/ups-mqtt/setup/, never to ~/ups-mqtt/ups-mqtt.service: hosts set
# up by an early first-install.sh have the installed unit symlinked to that
# file, and overwriting it with the repo's unit (User=SERVICE_USER) broke
# the live unit there.  install-unit.sh replaces such a symlink with a file.
SETUP_DIR="setup"
STAGE=$(mktemp -d "${TMPDIR:-/tmp}/ups-mqtt-deploy.XXXXXX")
trap 'rm -rf "${STAGE}"' EXIT
mkdir "${STAGE}/${SETUP_DIR}"
cp "${SERVICE}.service" scripts/install-unit.sh scripts/install-sudoers.sh "${STAGE}/${SETUP_DIR}/"
ship_setup() {
  echo "==> Shipping the unit and installers to ${TARGET}:~/${SERVICE}/${SETUP_DIR}/..."
  scp -q -r "${STAGE}/${SETUP_DIR}" "${TARGET}:~/${SERVICE}/"
}

# ---------------------------------------------------------------------------
# --install-unit / --install-sudoers: ship, then run that installer with a
# terminal so its confirmation prompt (and sudo's password) can ask.  Quoted
# as one remote command, so ~ is the remote home, not this machine's.
# ---------------------------------------------------------------------------
if [[ -n "$INSTALL" ]]; then
  ship_setup
  # Not exec: the EXIT trap must still remove the staging directory.
  rc=0
  ssh -t "${TARGET}" "sudo bash ~/${SERVICE}/${SETUP_DIR}/${INSTALL}" || rc=$?
  exit "${rc}"
fi
KEEP_VERSIONS=3
VERSION=$(date +%Y%m%d-%H%M%S)

# ---------------------------------------------------------------------------
# 1. Detect target architecture
# ---------------------------------------------------------------------------
echo "==> Detecting architecture on ${TARGET}..."
UNAME=$(ssh "${TARGET}" "uname -m")
case "${UNAME}" in
  x86_64)        GOOS=linux; GOARCH=amd64; GOARM="";  SUFFIX="linux-amd64"   ;;
  aarch64|arm64) GOOS=linux; GOARCH=arm64; GOARM="";  SUFFIX="linux-arm64"   ;;
  armv7l)        GOOS=linux; GOARCH=arm;   GOARM=7;   SUFFIX="linux-armv7"   ;;
  armv6l)        GOOS=linux; GOARCH=arm;   GOARM=6;   SUFFIX="linux-armv6"   ;;
  *)             echo "Unsupported architecture: ${UNAME}"; exit 1            ;;
esac
echo "    ${UNAME} → GOOS=${GOOS} GOARCH=${GOARCH}${GOARM:+ GOARM=${GOARM}}"

# ---------------------------------------------------------------------------
# 2. Check prerequisites
# ---------------------------------------------------------------------------
if ! ssh "${TARGET}" "[ -L /usr/local/bin/${SERVICE} ]" 2>/dev/null; then
  echo ""
  echo "  ERROR: /usr/local/bin/${SERVICE} not found on ${TARGET}."
  echo "  Run ./first-install.sh ${WHERE} first."
  echo ""
  exit 1
fi

# ---------------------------------------------------------------------------
# 3. Build
# ---------------------------------------------------------------------------
LOCAL_BIN="${SERVICE}-${SUFFIX}"
REMOTE_BIN="${SERVICE}-${VERSION}"

echo "==> Building ${LOCAL_BIN}..."
GOOS="${GOOS}" GOARCH="${GOARCH}" GOARM="${GOARM}" \
  go build -ldflags="-s -w" -o "${LOCAL_BIN}" ./cmd/${SERVICE}/
trap 'rm -f "${LOCAL_BIN}"; rm -rf "${STAGE}"' EXIT

# ---------------------------------------------------------------------------
# 4. Upload
# ---------------------------------------------------------------------------
echo "==> Uploading to ${TARGET}:~/${SERVICE}/${REMOTE_BIN}..."
scp -q "${LOCAL_BIN}" "${TARGET}:~/${SERVICE}/${REMOTE_BIN}"
# Step 9 checks whether the shipped unit needs installing.
ship_setup

# ---------------------------------------------------------------------------
# 5. Atomic swap: make executable and repoint the symlink
# ---------------------------------------------------------------------------
echo "==> Activating ${REMOTE_BIN}..."
ssh "${TARGET}" "
  chmod +x ~/${SERVICE}/${REMOTE_BIN}
  ln -sfn ~/${SERVICE}/${REMOTE_BIN} ~/${SERVICE}/${SERVICE}
"

# ---------------------------------------------------------------------------
# 6. Config — install only if the host has none
# ---------------------------------------------------------------------------
# Config is host-local state: each host names its own UPS and label, and two
# hosts sharing a client_id will evict each other from the broker. Overwriting
# it on every deploy meant shipping whichever config.toml happened to be in the
# local checkout, so deploying from one host's working copy would silently
# repoint another host's UPS. This matches first-install.sh, which has always
# only staged a config when the target lacked one.
#
# To change a deployed host's config, edit it on that host.
if ssh "${TARGET}" "[ -f /etc/${SERVICE}/config.toml ]" 2>/dev/null; then
  echo "==> Config present on ${TARGET} — leaving it untouched."
else
  if [ -f "config.toml" ]; then
    echo "==> No config on ${TARGET}; installing local config.toml..."
    < config.toml ssh "${TARGET}" "sudo tee /etc/${SERVICE}/config.toml > /dev/null"
  else
    echo "  ⚠  No config on ${TARGET} and no local config.toml — the service will not start."
  fi
fi

# ---------------------------------------------------------------------------
# 7. Restart
# ---------------------------------------------------------------------------
echo "==> Restarting ${SERVICE}..."
ssh "${TARGET}" "sudo systemctl daemon-reload && sudo systemctl restart ${SERVICE}"
sleep 5

STATUS=$(ssh "${TARGET}" "systemctl is-active ${SERVICE} 2>/dev/null || true")
echo "    Status: ${STATUS}"
if [ "${STATUS}" != "active" ]; then
  echo "  ⚠  Service is '${STATUS}' — showing recent logs (journal lines need the"
  echo "     adm or systemd-journal group on ${TARGET}; else: sudo journalctl -u ${SERVICE}):"
  ssh "${TARGET}" "systemctl status ${SERVICE} --no-pager -l"
  exit 1
fi

# ---------------------------------------------------------------------------
# 8. Prune old versions (keep KEEP_VERSIONS most recent)
# ---------------------------------------------------------------------------
echo "==> Pruning old versions (keeping ${KEEP_VERSIONS})..."
ssh "${TARGET}" "
  cd ~/${SERVICE}
  ls -t ${SERVICE}-2* 2>/dev/null \
    | tail -n +$((KEEP_VERSIONS + 1)) \
    | xargs -r rm --
"

# ---------------------------------------------------------------------------
# 9. Unit file — report only; installing it needs root
# ---------------------------------------------------------------------------
echo "==> Checking the installed unit against this checkout's..."
UNIT_CHECK=0
ssh "${TARGET}" "bash ~/${SERVICE}/${SETUP_DIR}/install-unit.sh --check" || UNIT_CHECK=$?

# ---------------------------------------------------------------------------
# 10. Done
# ---------------------------------------------------------------------------
echo ""
echo "==> Deployed ${VERSION} to ${TARGET} [${SUFFIX}]"
ssh "${TARGET}" "systemctl status ${SERVICE} --no-pager -l"
# install-unit.sh --check: 0 up to date, 1 differs, 2 error (no installed
# unit, no User=, or systemd-analyze rejects the new one).
case "${UNIT_CHECK}" in
  0) ;;
  1)
    echo ""
    echo "  ⚠  The installed unit differs from this checkout's (diff above). To install it:"
    echo "       ./deploy.sh --install-unit ${WHERE}"
    ;;
  *)
    echo ""
    echo "  ⚠  install-unit.sh --check failed (exit ${UNIT_CHECK}); see its output above."
    echo "     Do not install the unit until that is resolved."
    ;;
esac
