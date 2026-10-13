#!/usr/bin/env bash
# install-sudoers.sh — install the narrow passwordless-sudo rule deploy.sh needs.
#
# deploy.sh runs over ssh without a terminal, so the few root commands it
# needs must not ask for a password.  The rule allows exactly those:
#
#   systemctl daemon-reload
#   systemctl restart ups-mqtt
#   tee /etc/ups-mqtt/config.toml       (only when a host has no config yet)
#
# and nothing else.  It replaces an earlier rule that allowed any systemctl
# and journalctl command.  Any systemctl command is root (`systemctl edit`
# writes a unit as root; `link` then `start` runs anything); any journalctl
# command reads, and can delete, every log on the host.  Everything else
# goes through the user's normal, password-checked sudo.
#
# The rule is only that narrow while the ups-mqtt unit cannot be changed
# without root: a root-owned regular file whose User= is not root, with no
# drop-ins the user can write.  It warns when that is not so.
#
# It shows the diff, checks the rule with visudo, and installs it after a
# prompt.  The whole sudoers configuration is checked before (an already
# broken one is refused) and after (the previous rule is restored if it now
# fails).  Finally it lists passwordless grants the user still has from any
# other sudoers entry that leave them root or let them read any log: ALL,
# and systemctl or journalctl with no arguments or a wildcard.  It does not
# change those.
#
# Usage (on the host; reading /etc/sudoers.d needs root even for --check):
#   sudo ./install-sudoers.sh --check    # show the diff, change nothing
#   sudo ./install-sudoers.sh            # install, after a confirmation prompt
#   sudo ./install-sudoers.sh --yes      # install without prompting
#   ... --user NAME                      # default: the user who ran sudo
#
# The prompt needs a terminal: over ssh, that means ssh -t (deploy.sh
# --install-sudoers does it).  Without one, and without --yes, it refuses.
#
# Exit codes: 0 up to date (or installed), 1 changes needed (--check) or
# aborted at the prompt, 2 error.
#
# INSTALL_SUDOERS_ROOT prefixes every path, so tests can run against a
# fixture tree (where files are owned by whoever runs the tests, not root);
# INSTALL_SUDOERS_TTY=1 lets them answer the prompt from a file.
# See scripts/test/install-sudoers_test.sh.
set -euo pipefail

SERVICE="ups-mqtt"
ROOT="${INSTALL_SUDOERS_ROOT:-}"
SUDOERS_D="${ROOT}/etc/sudoers.d"
INSTALLED="${SUDOERS_D}/${SERVICE}"
SYSTEMCTL="/usr/bin/systemctl"
TEE="/usr/bin/tee"
CONFIG="/etc/${SERVICE}/config.toml"
UNIT="${ROOT}/etc/systemd/system/${SERVICE}.service"
# Who must own the unit for the rule to stay narrow.
OWNER=root
[[ -z "$ROOT" ]] || OWNER=$(id -un)

die() { echo "ERROR: $*" >&2; exit 2; }
say() { echo "==> $*"; }

usage() { sed -n '2,/^set -euo/p' "$0" | grep '^#' | sed 's/^# \{0,1\}//'; }

CHECK=0
YES=0
USER_NAME="${SUDO_USER:-}"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --check|--dry-run) CHECK=1 ;;
    --yes|-y)          YES=1 ;;
    --user)            [[ $# -ge 2 ]] || die "--user needs a name"
                       USER_NAME=$2; shift ;;
    -h|--help)         usage; exit 0 ;;
    *)                 usage >&2; die "unknown argument: $1" ;;
  esac
  shift
done

# ── Preconditions ──────────────────────────────────────────────────────────
if [[ -z "$ROOT" && $EUID -ne 0 ]]; then
  die "needs root, even for --check (/etc/sudoers.d is not readable otherwise): run with sudo"
fi
[[ -n "$USER_NAME" ]] || die "no user to grant: run it with sudo from that user's account, or pass --user NAME"
[[ "$USER_NAME" != root ]] || die "refusing to write a rule for root; pass --user NAME"
[[ "$USER_NAME" =~ ^[a-z_][a-z0-9_-]*$ ]] || die "not a plain user name: $USER_NAME"
id "$USER_NAME" >/dev/null 2>&1 || die "no such user: $USER_NAME"
for cmd in "$SYSTEMCTL" "$TEE"; do
  [[ -x "${ROOT}${cmd}" ]] || die "$cmd not found; the rule names it by that path"
done
[[ -d "$SUDOERS_D" ]] || die "${SUDOERS_D#"$ROOT"} does not exist"
command -v visudo >/dev/null || die "visudo not found"

# unit_warnings prints why daemon-reload + restart would give root: anyone
# who can edit the unit or a drop-in can set User=root and ExecStart.
unit_warnings() {
  local f
  if [[ ! -e "$UNIT" && ! -L "$UNIT" ]]; then
    return 0   # not installed yet; first-install.sh installs it as root
  fi
  if [[ -L "$UNIT" ]]; then
    echo "${UNIT#"$ROOT"} is a symlink (to $(readlink "$UNIT")); install it as a file: deploy.sh --install-unit"
  elif [[ -z "$(find "$UNIT" -maxdepth 0 -user "$OWNER" ! -perm -g+w ! -perm -o+w)" ]]; then
    echo "${UNIT#"$ROOT"} is not owned by $OWNER, or is group- or world-writable"
  fi
  if ! grep -Eq '^User=[^[:space:]]+' "$UNIT" || grep -Eq '^User=(root|0)[[:space:]]*$' "$UNIT"; then
    echo "${UNIT#"$ROOT"} has no User= or runs as root"
  fi
  if [[ -d "${UNIT}.d" ]]; then
    while IFS= read -r f; do
      echo "drop-in ${f#"$ROOT"} is a symlink, not owned by $OWNER, or writable by others"
    done < <(find "${UNIT}.d" -mindepth 1 \( -type l -o ! -user "$OWNER" -o -perm -g+w -o -perm -o+w \) 2>/dev/null)
  fi
}
warnings=$(unit_warnings)
if [[ -n "$warnings" ]]; then
  echo "  ⚠  With this rule, ${USER_NAME} could still become root through the unit:"
  echo "$warnings" | sed 's/^/       /'
  echo
fi


WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
RENDERED="$WORK/${SERVICE}"
cat > "$RENDERED" <<EOF
# Installed by ups-mqtt's scripts/install-sudoers.sh.  Only what deploy.sh
# runs without a terminal; anything else uses the normal, password-checked
# sudo.  Do not widen it: any systemctl command is root.
${USER_NAME} ALL=(root) NOPASSWD: ${SYSTEMCTL} daemon-reload, ${SYSTEMCTL} restart ${SERVICE}, ${TEE} ${CONFIG}
EOF

# ── Compare ────────────────────────────────────────────────────────────────
say "rule:   ${INSTALLED#"$ROOT"} (for ${USER_NAME})"
if [[ -f "$INSTALLED" ]] && cmp -s "$INSTALLED" "$RENDERED"; then
  say "up to date — nothing to do"
  exit 0
fi

echo
if [[ -f "$INSTALLED" ]]; then
  diff -u --label "installed ${INSTALLED#"$ROOT"}" --label "new" "$INSTALLED" "$RENDERED" || true
else
  echo "(nothing installed yet)"
  sed 's/^/+/' "$RENDERED"
fi
echo

if ! out=$(visudo -c -f "$RENDERED" 2>&1); then
  echo "$out" >&2
  die "visudo rejects the new rule; nothing changed"
fi
say "visudo -c: ok"

# The whole configuration must parse before anything changes: afterwards a
# failure would be blamed on, and undo, the new rule.
if ! out=$(visudo -c 2>&1); then
  echo "$out" >&2
  die "the sudoers configuration already fails visudo -c; fix that first. Nothing changed"
fi

if [[ $CHECK == 1 ]]; then
  echo "Would: install the rule above"
  exit 1
fi

# ── Apply ──────────────────────────────────────────────────────────────────
if [[ $YES == 0 && ! -t 0 && -z "${INSTALL_SUDOERS_TTY:-}" ]]; then
  die "no terminal to ask for confirmation on; nothing changed. Over ssh, use ssh -t (or pass --yes)"
fi

echo "About to replace ${INSTALLED#"$ROOT"} with the rule above."
echo "Your own password-checked sudo is not affected."
if [[ $YES == 0 ]]; then
  read -r -p "Continue? [y/N] " answer || answer=""
  case "$answer" in
    y|Y|yes|YES) ;;
    *) echo "Aborted; nothing changed."; exit 1 ;;
  esac
fi

# sudo ignores files in sudoers.d whose names contain a dot, so the new rule
# is staged under one and renamed into place.
BACKUP="$WORK/previous"
[[ -f "$INSTALLED" ]] && cp -p "$INSTALLED" "$BACKUP"
STAGED="${SUDOERS_D}/.${SERVICE}.new"
cp "$RENDERED" "$STAGED"
chmod 440 "$STAGED"
[[ -n "$ROOT" ]] || chown root:root "$STAGED"
mv -f "$STAGED" "$INSTALLED"

# A broken sudoers configuration locks everyone out of sudo, so check the
# whole of it again, and put the old rule back on failure.
if ! out=$(visudo -c 2>&1); then
  echo "$out" >&2
  if [[ -f "$BACKUP" ]]; then
    cp -p "$BACKUP" "$STAGED" && mv -f "$STAGED" "$INSTALLED"
    die "visudo -c fails with the new rule in place; the previous rule is restored"
  fi
  rm -f "$INSTALLED"
  die "visudo -c fails with the new rule in place; it has been removed"
fi
say "installed ${INSTALLED#"$ROOT"}; visudo -c: ok"

# ── What else still grants it ──────────────────────────────────────────────
# broad_grants reads `sudo -l` output and prints each passwordless command
# that is root, or reads any log: ALL, and systemctl or journalctl (any path)
# with no arguments or with a wildcard.  Entries are comma-separated; tags
# such as NOPASSWD: carry on to later entries until another tag changes them,
# and a runas spec "(user)" may start any entry.
broad_grants() {
  awk '
    function trim(x) { sub(/^[ \t]+/, "", x); sub(/[ \t]+$/, "", x); return x }
    /^[ \t]*\(/ {
      n = split($0, cmds, ","); nopw = 0
      for (i = 1; i <= n; i++) {
        c = trim(cmds[i])
        while (1) {
          if (match(c, /^\([^)]*\)[ \t]*/)) { c = substr(c, RLENGTH + 1); continue }
          if (match(c, /^[A-Z_]+:[ \t]*/)) {
            tag = substr(c, 1, RLENGTH); sub(/:.*/, "", tag)
            if (tag == "NOPASSWD") nopw = 1
            else if (tag == "PASSWD") nopw = 0
            c = substr(c, RLENGTH + 1); continue
          }
          break
        }
        c = trim(c)
        if (!nopw || c == "") continue
        if (c == "ALL") { print "ALL (root)"; continue }
        split(c, w, /[ \t]+/); base = w[1]; sub(/.*\//, "", base)
        if (base != "systemctl" && base != "journalctl") continue
        if (c != w[1] && index(c, "*") == 0) continue
        print c (base == "systemctl" ? " (root)" : " (read or delete any log)")
      }
    }'
}

broad=$(sudo -l -U "$USER_NAME" 2>/dev/null | broad_grants | sort -u || true)
if [[ -n "$broad" ]]; then
  echo
  echo "  ⚠  ${USER_NAME} can still run these without a password, granted outside"
  echo "     ${INSTALLED#"$ROOT"} (this script changes only that file):"
  echo "$broad" | sed 's/^/       /'
else
  say "no passwordless ALL, or systemctl or journalctl with any arguments, left for ${USER_NAME}"
fi
