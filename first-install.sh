#!/usr/bin/env bash
# first-install.sh — build, upload, and prepare ups-mqtt on a remote host.
#
# This script does everything that doesn't need sudo.  At the end it prints
# a single command to run in your terminal to complete the installation.
#
# Usage:
#   ./first-install.sh --site NAME        # a site in the local sites file
#   ./first-install.sh --host USER@HOST   # a host not in it
#
# The sites file is described in deploy-sites.sh.  There is no default
# host, so an install always names the host it changes.
set -euo pipefail
cd "$(dirname "$0")"   # every path below is relative to the checkout

SITE=""
HOST=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --site)    [[ $# -ge 2 && -n "$2" && "$2" != -* ]] || { echo "ERROR: --site needs a name" >&2; exit 2; }
               [[ -z "$SITE" ]] || { echo "ERROR: --site given twice" >&2; exit 2; }
               SITE=$2; shift ;;
    --host)    [[ $# -ge 2 && -n "$2" && "$2" != -* ]] || { echo "ERROR: --host needs USER@HOST" >&2; exit 2; }
               [[ -z "$HOST" ]] || { echo "ERROR: --host given twice" >&2; exit 2; }
               HOST=$2; shift ;;
    -h|--help) sed -n '2,/^set -euo/p' "$(basename "$0")" | grep '^#' | sed 's/^# \{0,1\}//'; exit 0 ;;
    -*)        echo "ERROR: unknown argument: $1" >&2; exit 2 ;;
    *)         echo "ERROR: name the host with --site NAME or --host USER@HOST, not '$1'" >&2; exit 2 ;;
  esac
  shift
done
# shellcheck source=deploy-sites.sh
source ./deploy-sites.sh
TARGET=$(resolve_target "$SITE" "$HOST") || exit 2
if [[ -n "$HOST" ]]; then WHERE="--host ${HOST}"; else WHERE="--site ${SITE}"; fi
SERVICE="ups-mqtt"
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
# 2. Build
# ---------------------------------------------------------------------------
LOCAL_BIN="${SERVICE}-${SUFFIX}"
REMOTE_BIN="${SERVICE}-${VERSION}"

echo "==> Building ${LOCAL_BIN}..."
GOOS="${GOOS}" GOARCH="${GOARCH}" GOARM="${GOARM}" \
  go build -ldflags="-s -w" -o "${LOCAL_BIN}" ./cmd/${SERVICE}/
trap 'rm -f "${LOCAL_BIN}"' EXIT

# ---------------------------------------------------------------------------
# 3. Upload binary, service file, setup script, and config
# ---------------------------------------------------------------------------
echo "==> Uploading files to ${TARGET}:~/${SERVICE}/..."
# The unit and installers go in setup/, as deploy.sh ships them: never to
# ~/ups-mqtt/ups-mqtt.service, which an early install symlinked the live
# unit to.
ssh "${TARGET}" "mkdir -p ~/${SERVICE}/setup"
scp -q "${LOCAL_BIN}"       "${TARGET}:~/${SERVICE}/${REMOTE_BIN}"
scp -q remote-setup.sh      "${TARGET}:~/${SERVICE}/remote-setup.sh"
scp -q "${SERVICE}.service" scripts/install-unit.sh scripts/install-sudoers.sh \
  "${TARGET}:~/${SERVICE}/setup/"

# Activate the versioned symlink (no sudo needed — it's in the user's home).
ssh "${TARGET}" "
  chmod +x ~/${SERVICE}/${REMOTE_BIN} ~/${SERVICE}/remote-setup.sh
  ln -sfn ~/${SERVICE}/${REMOTE_BIN} ~/${SERVICE}/${SERVICE}
"

# Upload config if /etc/ups-mqtt/config.toml doesn't already exist.
CONFIG_EXISTS=$(ssh "${TARGET}" \
  "[ -f /etc/${SERVICE}/config.toml ] && echo yes || echo no" 2>/dev/null || echo no)

if [ "${CONFIG_EXISTS}" = "no" ]; then
  if [ -f "config.toml" ]; then
    echo "==> Staging config.toml..."
    scp -q config.toml "${TARGET}:~/${SERVICE}/config.toml.pending"
  elif [ -f "config.toml.example" ]; then
    echo "==> Staging config.toml.example (edit before starting the service)..."
    scp -q config.toml.example "${TARGET}:~/${SERVICE}/config.toml.pending"
  fi
else
  echo "    /etc/${SERVICE}/config.toml already present — skipping."
fi

# ---------------------------------------------------------------------------
# 4. Print the one command the user needs to run
# ---------------------------------------------------------------------------
echo ""
echo "  All files uploaded. Now run this command in your terminal to complete"
echo "  the installation (you'll be prompted for your sudo password once):"
echo ""
echo "    ssh -t ${TARGET} 'sudo bash ~/${SERVICE}/remote-setup.sh'"
echo ""
echo "  (Append --check inside the quotes to see its plan first; it changes nothing.)"
echo ""
echo "  After that, all future updates are fully automated:"
echo "    ./deploy.sh ${WHERE}"
echo ""
