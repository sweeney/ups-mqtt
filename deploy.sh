#!/usr/bin/env bash
# deploy.sh — build, ship, and restart ups-mqtt on a remote host.
#
# Usage:
#   ./deploy.sh --site NAME        # deploy to a site's host
#   ./deploy.sh --host USER@HOST   # deploy to a host not in the sites file
#   ./deploy.sh --site NAME --check
#                                  # report what a deploy would do, change
#                                  # nothing (no build, no root, no restart)
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
#   --check            read-only: the deployed build, the prerequisites, the
#                      passwordless sudo the deploy needs, the service, and
#                      the unit diff.  Exit 0 ready to deploy, 1 something
#                      needs attention first, 2 error.
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
CHECK=0
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
    --check|--dry-run) CHECK=1 ;;
    -h|--help)         sed -n '2,/^set -euo/p' "$(basename "$0")" | grep '^#' | sed 's/^# \{0,1\}//'; exit 0 ;;
    -*)                echo "ERROR: unknown argument: $1" >&2; exit 2 ;;
    *)                 echo "ERROR: name the host with --site NAME or --host USER@HOST, not '$1'" >&2; exit 2 ;;
  esac
  shift
done
[[ $CHECK == 0 || -z "$INSTALL" ]] || { echo "ERROR: --check reports on a deploy; the installers have their own (run on the host)" >&2; exit 2; }
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

# ---------------------------------------------------------------------------
# --check: report, change nothing.  One ssh reads the host's state; a second
# streams this checkout's unit and install-unit.sh into a temporary directory
# there, runs its --check and removes it, so ~/ups-mqtt/setup/ is untouched.
# ---------------------------------------------------------------------------
if [[ $CHECK == 1 ]]; then
  echo "==> Checking ${TARGET} (read-only)..."
  ATTENTION=0
  attention() { echo "  ⚠  $*"; ATTENTION=1; }
  # Each line is KEY VALUE; "sudoers" lines are the host's sudo -n -l
  # listing, read below for what may run without a password.
  PROBE=$(ssh "${TARGET}" "
    echo arch \$(uname -m)
    echo link \$(readlink /usr/local/bin/${SERVICE} 2>/dev/null || echo none)
    echo current \$(readlink ~/${SERVICE}/${SERVICE} 2>/dev/null || echo none)
    echo versions \$(ls -d ~/${SERVICE}/${SERVICE}-2* 2>/dev/null | wc -l)
    echo config \$([ -f /etc/${SERVICE}/config.toml ] && echo present || echo absent)
    echo active \$(systemctl is-active ${SERVICE} 2>/dev/null || true)
    sudo -n -l 2>/dev/null | sed 's/^/sudoers /'
  ") || { echo "ERROR: cannot read ${TARGET}'s state over ssh" >&2; exit 2; }
  field() { awk -v k="$1" '$1 == k {sub(/^[^ ]+ ?/, ""); print; exit}' <<<"${PROBE}"; }

  case "$(field arch)" in
    x86_64|aarch64|arm64|armv7l|armv6l) echo "    arch:      $(field arch)" ;;
    *) attention "unsupported architecture: $(field arch)" ;;
  esac
  echo "    would ship: $(git describe --always --dirty 2>/dev/null || echo "an unknown commit")"
  if [[ "$(field link)" == none ]]; then
    attention "/usr/local/bin/${SERVICE} not found: run ./first-install.sh ${WHERE} first"
  else
    echo "    deployed:  $(basename "$(field current)")"
  fi
  VERSIONS=$(field versions)
  # Never do arithmetic on text from the host unchecked: bash would evaluate it.
  [[ ${VERSIONS} =~ ^[0-9]+$ ]] || { echo "ERROR: unreadable version count from ${TARGET}: '${VERSIONS}'" >&2; exit 2; }
  PRUNE=$(( VERSIONS + 1 > KEEP_VERSIONS ? VERSIONS + 1 - KEEP_VERSIONS : 0 ))
  echo "    versions:  ${VERSIONS} kept on the host; a deploy adds one and prunes ${PRUNE}"
  if [[ "$(field config)" == present ]]; then
    echo "    config:    present (left untouched)"
  elif [[ -f config.toml ]]; then
    echo "    config:    absent; this checkout's config.toml would be installed"
  else
    attention "no config on the host and no local config.toml: the service would not start"
  fi
  if [[ "$(field active)" == active ]]; then
    echo "    service:   active (a deploy restarts it)"
  else
    attention "service is '$(field active)'"
  fi
  # sudo -l with a command only says whether it is allowed at all, password
  # or not, so read the entries themselves.  An entry is "(RUNAS) cmd, cmd",
  # each cmd possibly prefixed by tags (NOPASSWD:, PASSWD:, SETENV: …); a
  # NOPASSWD: or PASSWD: tag holds for the commands after it until the next.
  # Only entries that run as root count.  Prints each passwordless command.
  NOPASSWD=$(awk '$1 == "sudoers" && match($0, /\([^)]*\)/) {
      runas = substr($0, RSTART + 1, RLENGTH - 2); sub(/ *:.*$/, "", runas)
      if (runas ~ /!/ || runas !~ /(^|, *)(root|ALL)(,|$)/) next
      rest = substr($0, RSTART + RLENGTH); sub(/^ +/, "", rest)
      n = split(rest, cmds, /, */); np = 0
      for (i = 1; i <= n; i++) {
        c = cmds[i]
        while (match(c, /^[A-Z_]+: */)) {
          tag = substr(c, 1, RLENGTH); sub(/: *$/, "", tag)
          if (tag == "NOPASSWD") np = 1; else if (tag == "PASSWD") np = 0
          c = substr(c, RLENGTH + 1)
        }
        if (np || c ~ /^!/) print c
      }
    }' <<<"${PROBE}")
  MISSING=""
  # sudo finds a bare command on its secure_path, so only system directories.
  # A negated command (!cmd) may carve the very one needed out of an ALL,
  # so with any negation present ALL proves nothing.
  ALL="ALL|"
  grep -q '^!' <<<"${NOPASSWD}" && ALL=""
  for c in "systemctl daemon-reload" "systemctl restart ${SERVICE}" "tee /etc/${SERVICE}/config.toml"; do
    grep -qxE "${ALL}(/usr)?/s?bin/${c//./\\.}" <<<"${NOPASSWD}" || MISSING+="${c}"$'\n'
  done
  MISSING=${MISSING%$'\n'}
  if [[ -z "${MISSING}" ]]; then
    echo "    sudo:      the deploy's root commands need no password"
  else
    while read -r c; do attention "sudo would ask for a password for: ${c}"; done <<<"${MISSING}"
    echo "     install the rule: ./deploy.sh --install-sudoers ${WHERE}"
  fi

  echo "==> Unit:"
  rc=0
  tar -C "${STAGE}/${SETUP_DIR}" -cf - "${SERVICE}.service" install-unit.sh \
    | ssh "${TARGET}" 'd=$(mktemp -d) && trap "rm -rf \"$d\"" EXIT && tar -C "$d" -xf - && bash "$d/install-unit.sh" --check "$d/'"${SERVICE}"'.service"' \
    || rc=$?
  case "${rc}" in
    0) ;;
    1) attention "the installed unit differs (diff above); after deploying: ./deploy.sh --install-unit ${WHERE}" ;;
    *) attention "install-unit.sh --check failed (exit ${rc}); see above" ;;
  esac

  echo ""
  if [[ ${ATTENTION} == 0 ]]; then
    echo "==> Ready to deploy: ./deploy.sh ${WHERE}"
  else
    echo "==> Needs attention before (or after) deploying; see ⚠ above."
  fi
  exit "${ATTENTION}"
fi
# A --check must never go on to deploy: an error that abandons the block
# above (bash does that for an arithmetic error, set -e or not) stops here.
[[ $CHECK == 0 ]] || { echo "ERROR: --check stopped unexpectedly; nothing was changed" >&2; exit 2; }
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
