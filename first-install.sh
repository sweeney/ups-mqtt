#!/usr/bin/env bash
# first-install.sh — build, upload, and prepare ups-mqtt on a remote host.
#
# This script does everything that doesn't need sudo.  At the end it prints
# a single command to run in your terminal to complete the installation.
#
# Usage:
#   ./first-install.sh                  # default host: sweeney@garibaldi
#   ./first-install.sh user@host
set -euo pipefail

TARGET="${1:-sweeney@garibaldi}"
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
ssh "${TARGET}" "mkdir -p ~/${SERVICE}"
scp -q "${LOCAL_BIN}"       "${TARGET}:~/${SERVICE}/${REMOTE_BIN}"
scp -q "${SERVICE}.service" "${TARGET}:~/${SERVICE}/${SERVICE}.service"
scp -q remote-setup.sh      "${TARGET}:~/${SERVICE}/remote-setup.sh"

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
echo "  After that, all future updates are fully automated:"
echo "    ./deploy.sh ${TARGET}"
echo ""
