#!/usr/bin/env bash
# deploy.sh — build, ship, and restart ups-mqtt on a remote host.
#
# Usage:
#   ./deploy.sh                    # deploy to default host (sweeney@garibaldi)
#   ./deploy.sh user@host          # deploy to a specific host
#
# Prerequisites on the target host (one-time, run first-install.sh):
#   - /etc/ups-mqtt/config.toml exists
#   - /usr/local/bin/ups-mqtt symlink exists
#   - /etc/systemd/system/ups-mqtt.service symlink exists
#   - passwordless sudo for systemctl:
#       sweeney ALL=(ALL) NOPASSWD: /usr/bin/systemctl
#
# See first-install.sh for the one-time setup procedure.
set -euo pipefail

TARGET="${1:-sweeney@garibaldi}"
SERVICE="ups-mqtt"
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
  echo "  Run ./first-install.sh ${TARGET} first."
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
trap 'rm -f "${LOCAL_BIN}"' EXIT

# ---------------------------------------------------------------------------
# 4. Upload
# ---------------------------------------------------------------------------
echo "==> Uploading to ${TARGET}:~/${SERVICE}/${REMOTE_BIN}..."
scp -q "${LOCAL_BIN}" "${TARGET}:~/${SERVICE}/${REMOTE_BIN}"

# ---------------------------------------------------------------------------
# 5. Atomic swap: make executable and repoint the symlink
# ---------------------------------------------------------------------------
echo "==> Activating ${REMOTE_BIN}..."
ssh "${TARGET}" "
  chmod +x ~/${SERVICE}/${REMOTE_BIN}
  ln -sfn ~/${SERVICE}/${REMOTE_BIN} ~/${SERVICE}/${SERVICE}
"

# ---------------------------------------------------------------------------
# 6. Restart
# ---------------------------------------------------------------------------
echo "==> Restarting ${SERVICE}..."
ssh "${TARGET}" "sudo systemctl daemon-reload && sudo systemctl restart ${SERVICE}"
sleep 5

STATUS=$(ssh "${TARGET}" "systemctl is-active ${SERVICE} 2>/dev/null || true")
echo "    Status: ${STATUS}"
if [ "${STATUS}" != "active" ]; then
  echo "  ⚠  Service is '${STATUS}' — showing recent logs:"
  ssh "${TARGET}" "sudo systemctl status ${SERVICE} --no-pager -l"
  exit 1
fi

# ---------------------------------------------------------------------------
# 7. Prune old versions (keep KEEP_VERSIONS most recent)
# ---------------------------------------------------------------------------
echo "==> Pruning old versions (keeping ${KEEP_VERSIONS})..."
ssh "${TARGET}" "
  cd ~/${SERVICE}
  ls -t ${SERVICE}-2* 2>/dev/null \
    | tail -n +$((KEEP_VERSIONS + 1)) \
    | xargs -r rm --
"

# ---------------------------------------------------------------------------
# 8. Done
# ---------------------------------------------------------------------------
echo ""
echo "==> Deployed ${VERSION} to ${TARGET} [${SUFFIX}]"
ssh "${TARGET}" "sudo systemctl status ${SERVICE} --no-pager -l"
