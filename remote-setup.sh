#!/usr/bin/env bash
# remote-setup.sh — run once on the target machine with sudo.
# Uploaded by first-install.sh, which prints the command to run it:
#
#   ssh -t <host> 'sudo bash ~/ups-mqtt/remote-setup.sh'
#   ssh -t <host> 'sudo bash ~/ups-mqtt/remote-setup.sh --check'   # plan only
#
# Installs the config staged by first-install.sh, the unit (from
# ~/ups-mqtt/setup/) and the deploy user's narrow sudoers rule, then enables
# and starts the service.  Everything it needs, including what the sudoers
# installer needs, is checked before anything is written; re-running it is
# safe.
#
# REMOTE_SETUP_ROOT prefixes the system paths (/etc, /usr/local/bin) and
# skips the root check, so tests can run against a fixture tree.  See
# scripts/test/remote-setup_test.sh.
set -euo pipefail

SERVICE="ups-mqtt"
ROOT="${REMOTE_SETUP_ROOT:-}"
CHECK=0
for arg in "$@"; do
  case "$arg" in
    --check|--dry-run) CHECK=1 ;;
    -h|--help)         sed -n '2,/^set -euo/p' "$0" | grep '^#' | sed 's/^# \{0,1\}//'; exit 0 ;;
    *)                 echo "  ERROR: unknown argument: $arg" >&2; exit 2 ;;
  esac
done
die() { echo "  ERROR: $*" >&2; exit 2; }

# ── Preconditions: nothing is written until all of these hold ─────────────
[[ -n "$ROOT" || $EUID -eq 0 ]] || die "run this with sudo: sudo bash $0"
# The service runs as this user and gets passwordless sudo for deploys: it
# must be the account deploy.sh logs in as, never root, and never a guess.
INSTALL_USER="${SUDO_USER:-$(logname 2>/dev/null || true)}"
[[ -n "${INSTALL_USER}" ]] || die "cannot tell which user to install for; run it with sudo from the deploy user's account"
[[ "${INSTALL_USER}" != root ]] || die "run this with sudo from the deploy user's account, not as root."
INSTALL_HOME=$(getent passwd "${INSTALL_USER}" | cut -d: -f6 || true)
[[ -n "${INSTALL_HOME}" && -d "${INSTALL_HOME}" ]] || die "no home directory for ${INSTALL_USER}"
HERE="${INSTALL_HOME}/${SERVICE}"
UNIT_SRC="${HERE}/setup/${SERVICE}.service"
SUDOERS_SH="${HERE}/setup/install-sudoers.sh"
[[ -x "${HERE}/${SERVICE}" ]] || die "${HERE}/${SERVICE} is missing, a broken link or not executable: run first-install.sh first"
[[ -f "${UNIT_SRC}" ]] || die "${UNIT_SRC} not found: run first-install.sh first"
grep -qx 'User=SERVICE_USER' "${UNIT_SRC}" || die "${UNIT_SRC} has no 'User=SERVICE_USER' line to fill in"
[[ -f "${SUDOERS_SH}" ]] || die "${SUDOERS_SH} not found: run first-install.sh first"
# The sudoers installer checks its own preconditions (visudo, /etc/sudoers.d,
# the commands the rule names); its --check runs them now rather than after
# the config and unit are in place.  1 only means the rule is not there yet.
rc=0
SUDOERS_PLAN=$(bash "${SUDOERS_SH}" --check --user "${INSTALL_USER}" 2>&1) || rc=$?
if [[ $rc -ge 2 ]]; then
  echo "${SUDOERS_PLAN}" >&2
  die "the sudoers rule cannot be installed (above); nothing was changed"
fi

CONFIG="${ROOT}/etc/${SERVICE}/config.toml"
PENDING="${HERE}/config.toml.pending"
UNIT_DST="${ROOT}/etc/systemd/system/${SERVICE}.service"
LINK="${ROOT}/usr/local/bin/${SERVICE}"

echo "  Installing ${SERVICE} for user ${INSTALL_USER} (home: ${INSTALL_HOME})"
if [[ $CHECK == 1 ]]; then
  if [[ -f "${PENDING}" && -f "${CONFIG}" ]]; then echo "  would REPLACE /etc/${SERVICE}/config.toml with the staged config"
  elif [[ -f "${PENDING}" ]]; then echo "  would install the staged config → /etc/${SERVICE}/config.toml"
  elif [[ -f "${CONFIG}" ]]; then echo "  config present; left untouched"
  else echo "  ⚠  no config staged or installed: the service would not start"; fi
  echo "  would link /usr/local/bin/${SERVICE} → ${HERE}/${SERVICE}"
  echo "  would install the unit (User=${INSTALL_USER}) and the sudoers rule, then enable and start ${SERVICE}"
  echo "  sudoers:"
  sed 's/^/    /' <<<"${SUDOERS_PLAN}"
  exit 0
fi

# Config: a staged one (first-install.sh found none on the host) is moved
# into place, replacing any installed since; otherwise the host's is kept.
mkdir -p "${ROOT}/etc/${SERVICE}"
if [ -f "${PENDING}" ]; then
  mv "${PENDING}" "${CONFIG}"
  echo "  Installed config → /etc/${SERVICE}/config.toml"
elif [ ! -f "${CONFIG}" ]; then
  echo "  ⚠  No config at /etc/${SERVICE}/config.toml — edit it before starting."
fi

# Symlink binary into system path.
ln -sfn "${HERE}/${SERVICE}" "${LINK}"
echo "  Symlinked binary  → /usr/local/bin/${SERVICE}"

# Patch the service file's User= placeholder and install it as a root-owned
# file.  Rendered beside the target and renamed: an early install symlinked
# the unit into the user's home, and writing through that link would change
# (or, redirecting onto its own source, empty) the user's copy instead.
sed "s/User=SERVICE_USER/User=${INSTALL_USER}/" "${UNIT_SRC}" > "${UNIT_DST}.tmp"
chmod 644 "${UNIT_DST}.tmp"
mv -f "${UNIT_DST}.tmp" "${UNIT_DST}"
echo "  Installed service → /etc/systemd/system/${SERVICE}.service (User=${INSTALL_USER})"

# Passwordless sudo for exactly the root commands deploy.sh runs, no more.
bash "${SUDOERS_SH}" --yes --user "${INSTALL_USER}"

# Enable and start.
systemctl daemon-reload
systemctl enable "${SERVICE}"
systemctl start "${SERVICE}"
echo "  Service enabled and started."
