#!/usr/bin/env bash
# remote-setup.sh — run once on the target machine with sudo.
# Uploaded and invoked by first-install.sh; not intended to be run directly.
#
#   sudo bash ~/ups-mqtt/remote-setup.sh
set -euo pipefail

SERVICE="ups-mqtt"
INSTALL_USER="${SUDO_USER:-$(logname 2>/dev/null || echo sweeney)}"
INSTALL_HOME=$(getent passwd "${INSTALL_USER}" | cut -d: -f6)

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

# Patch the service file's User= placeholder, then symlink it.
sed "s/User=SERVICE_USER/User=${INSTALL_USER}/" \
    "${INSTALL_HOME}/${SERVICE}/${SERVICE}.service" \
    > /etc/systemd/system/${SERVICE}.service
echo "  Installed service → /etc/systemd/system/${SERVICE}.service (User=${INSTALL_USER})"

# Passwordless sudo for systemctl so deploy.sh works without a password.
# Scoped to systemctl only.
SUDOERS_FILE="/etc/sudoers.d/${SERVICE}"
echo "${INSTALL_USER} ALL=(ALL) NOPASSWD: /usr/bin/systemctl, /usr/bin/journalctl" > "${SUDOERS_FILE}"
chmod 440 "${SUDOERS_FILE}"
visudo -c -f "${SUDOERS_FILE}" > /dev/null
echo "  Added sudoers rule → ${SUDOERS_FILE}"

# Enable and start.
systemctl daemon-reload
systemctl enable "${SERVICE}"
systemctl start "${SERVICE}"
echo "  Service enabled and started."
