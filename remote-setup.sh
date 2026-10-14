#!/usr/bin/env bash
# remote-setup.sh — run once on the target machine with sudo.
# Uploaded by first-install.sh, which prints the command to run it:
#
#   ssh -t <host> 'sudo bash ~/ups-mqtt/remote-setup.sh'
#
# Installs the config staged by first-install.sh, the unit (from
# ~/ups-mqtt/setup/) and the deploy user's narrow sudoers rule, then enables
# and starts the service.
set -euo pipefail

SERVICE="ups-mqtt"
INSTALL_USER="${SUDO_USER:-$(logname 2>/dev/null || echo sweeney)}"
INSTALL_HOME=$(getent passwd "${INSTALL_USER}" | cut -d: -f6)
# The service runs as this user and gets passwordless sudo for deploys: it
# must be the account deploy.sh logs in as, never root.  Checked before
# anything is written.
if [ "${INSTALL_USER}" = root ]; then
  echo "  ERROR: run this with sudo from the deploy user's account, not as root." >&2
  exit 2
fi

echo "  Installing ${SERVICE} for user ${INSTALL_USER} (home: ${INSTALL_HOME})"

# Config directory (leave existing config untouched).
mkdir -p /etc/${SERVICE}
if [ -f "${INSTALL_HOME}/${SERVICE}/config.toml.pending" ]; then
  mv "${INSTALL_HOME}/${SERVICE}/config.toml.pending" /etc/${SERVICE}/config.toml
  echo "  Installed config → /etc/${SERVICE}/config.toml"
elif [ ! -f /etc/${SERVICE}/config.toml ]; then
  echo "  ⚠  No config at /etc/${SERVICE}/config.toml — edit it before starting."
fi

# Symlink binary into system path.
ln -sf "${INSTALL_HOME}/${SERVICE}/${SERVICE}" /usr/local/bin/${SERVICE}
echo "  Symlinked binary  → /usr/local/bin/${SERVICE}"

# Patch the service file's User= placeholder and install it as a root-owned
# file.  Rendered beside the target and renamed: an early install symlinked
# the unit into the user's home, and writing through that link would change
# (or, redirecting onto its own source, empty) the user's copy instead.
UNIT_DST="/etc/systemd/system/${SERVICE}.service"
sed "s/User=SERVICE_USER/User=${INSTALL_USER}/" \
    "${INSTALL_HOME}/${SERVICE}/setup/${SERVICE}.service" > "${UNIT_DST}.tmp"
chmod 644 "${UNIT_DST}.tmp"
mv -f "${UNIT_DST}.tmp" "${UNIT_DST}"
echo "  Installed service → /etc/systemd/system/${SERVICE}.service (User=${INSTALL_USER})"

# Passwordless sudo for exactly the root commands deploy.sh runs, no more.
bash "$(dirname "$0")/setup/install-sudoers.sh" --yes --user "${INSTALL_USER}"

# Enable and start.
systemctl daemon-reload
systemctl enable "${SERVICE}"
systemctl start "${SERVICE}"
echo "  Service enabled and started."
