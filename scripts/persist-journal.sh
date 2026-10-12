#!/usr/bin/env bash
# persist-journal.sh — keep the systemd journal across reboots and power loss.
#
# Raspberry Pi OS ships /usr/lib/systemd/journald.conf.d/40-rpi-volatile-storage.conf
# (Storage=volatile), which keeps the journal in RAM only.  On a host whose job
# is to report power cuts, that means a power cut erases its own evidence: after
# the reboot there is no record of when the outage began or when the host died.
#
# This installs a drop-in that sorts after the RPi one and wins, then restarts
# journald and flushes the in-RAM journal to disk.
#
# Usage (on the host):
#   ./persist-journal.sh --check     # report only, changes nothing, no root needed
#   sudo ./persist-journal.sh        # apply, after a confirmation prompt
#   sudo ./persist-journal.sh --yes  # apply without prompting
#
# Exit codes: 0 already persistent (or applied), 1 changes needed (--check) or
# aborted at the prompt, 2 error.
#
# PERSIST_JOURNAL_ROOT prefixes every path, so tests can run against a fixture
# tree.  See scripts/test/persist-journal_test.sh.
set -euo pipefail

ROOT="${PERSIST_JOURNAL_ROOT:-}"
DROPIN_NAME="60-persistent-storage.conf"
DROPIN="${ROOT}/etc/systemd/journald.conf.d/${DROPIN_NAME}"
JOURNAL_DIR="${ROOT}/var/log/journal"

# SyncIntervalSec defaults to 5 min: anything logged in the five minutes
# before a sudden power loss would never reach the card.  10 s narrows that
# window; journald only writes when there is something new, so a quiet host
# costs little.  SystemMaxUse bounds the SD card footprint.
DESIRED='# Installed by ups-mqtt scripts/persist-journal.sh.
# Overrides 40-rpi-volatile-storage.conf (Storage=volatile) so the journal
# survives a power cut, which is exactly when it is needed.
[Journal]
Storage=persistent
SyncIntervalSec=10s
SystemMaxUse=64M'

die() { echo "ERROR: $*" >&2; exit 2; }
say() { echo "==> $*"; }

usage() { sed -n '2,/^set -euo/p' "$0" | grep '^#' | sed 's/^# \{0,1\}//'; }

CHECK=0
YES=0
for arg in "$@"; do
  case "$arg" in
    --check|--dry-run) CHECK=1 ;;
    --yes|-y)          YES=1 ;;
    -h|--help)         usage; exit 0 ;;
    *)                 usage >&2; die "unknown argument: $arg" ;;
  esac
done

# effective_storage prints the Storage= value journald will actually use,
# resolving drop-ins as systemd does: a file in /etc masks one of the same name
# in /run or /usr/lib, drop-ins apply in filename order after the main file,
# and the last assignment wins.  With an argument, that file is treated as if
# it were already installed as our drop-in, to project the result of applying.
effective_storage() {
  local value="auto" f name
  local -A chosen=()
  if [[ -f "${ROOT}/etc/systemd/journald.conf" ]]; then
    value=$(last_storage "${ROOT}/etc/systemd/journald.conf" "$value")
  fi
  # Lowest precedence first, so a higher-precedence directory overwrites.
  for dir in usr/lib/systemd run/systemd etc/systemd; do
    for f in "${ROOT}/${dir}/journald.conf.d/"*.conf; do
      [[ -f "$f" ]] || continue
      chosen["$(basename "$f")"]="$f"
    done
  done
  if [[ $# -gt 0 ]]; then chosen["$DROPIN_NAME"]="$1"; fi
  for name in $(printf '%s\n' "${!chosen[@]}" | sort); do
    value=$(last_storage "${chosen[$name]}" "$value")
  done
  echo "$value"
}

# last_storage prints the last active Storage= in file, or fallback if none.
last_storage() {
  local v
  v=$(sed -n 's/^[[:space:]]*Storage[[:space:]]*=[[:space:]]*\([^[:space:]#]*\).*/\1/p' "$1" | tail -1)
  echo "${v:-$2}"
}

journal_active() {
  local id
  id=$(tr -d '[:space:]' < "${ROOT}/etc/machine-id" 2>/dev/null) || return 1
  [[ -n "$id" && -f "${JOURNAL_DIR}/${id}/system.journal" ]]
}

# ── Inspect ────────────────────────────────────────────────────────────────
[[ -d "${ROOT}/etc/systemd" ]] || die "${ROOT}/etc/systemd not found — is this a systemd host?"
[[ -s "${ROOT}/etc/machine-id" ]] || die "${ROOT}/etc/machine-id missing or empty"

current=$(effective_storage)
dropin_ok=0
if [[ -f "$DROPIN" ]] && [[ "$(cat "$DROPIN")" == "$DESIRED" ]]; then dropin_ok=1; fi
active=0
if journal_active; then active=1; fi

say "effective Storage=${current}"
say "drop-in ${DROPIN#"$ROOT"}: $([[ $dropin_ok == 1 ]] && echo present || echo missing or out of date)"
say "persistent journal: $([[ $active == 1 ]] && echo being written || echo not active)"

# Would our drop-in actually win?  A later-sorting file setting Storage would
# override it, and writing ours would change nothing.  Check this before
# touching anything.
if [[ $dropin_ok == 0 ]]; then
  proposed=$(mktemp)
  trap 'rm -f "$proposed"' EXIT
  printf '%s\n' "$DESIRED" > "$proposed"
  projected=$(effective_storage "$proposed")
  if [[ "$projected" != "persistent" ]]; then
    die "a drop-in sorting after ${DROPIN_NAME} sets Storage=${projected}; installing ours would not take effect. Resolve that first."
  fi
fi

if [[ $dropin_ok == 1 && $active == 1 && "$current" == "persistent" ]]; then
  say "already persistent — nothing to do"
  exit 0
fi

if [[ $CHECK == 1 ]]; then
  echo "Would:"
  [[ $dropin_ok == 1 ]] || echo "  - write ${DROPIN#"$ROOT"} (Storage=persistent, SyncIntervalSec=10s, SystemMaxUse=64M)"
  echo "  - restart systemd-journald and flush the RAM journal to ${JOURNAL_DIR#"$ROOT"}"
  exit 1
fi

# ── Apply ──────────────────────────────────────────────────────────────────
if [[ -z "$ROOT" && $EUID -ne 0 ]]; then
  die "applying needs root: run with sudo (or use --check)"
fi

echo
echo "About to:"
echo "  - write ${DROPIN#"$ROOT"}"
echo "  - restart systemd-journald (logging pauses for about a second; services keep running)"
echo "  - flush the in-RAM journal to ${JOURNAL_DIR#"$ROOT"}"
if [[ $YES == 0 ]]; then
  read -r -p "Continue? [y/N] " answer || answer=""
  case "$answer" in
    y|Y|yes|YES) ;;
    *) echo "Aborted; nothing changed."; exit 1 ;;
  esac
fi

if [[ $dropin_ok == 0 ]]; then
  mkdir -p "$(dirname "$DROPIN")"
  printf '%s\n' "$DESIRED" > "${DROPIN}.tmp"
  mv -f "${DROPIN}.tmp" "$DROPIN"
  say "wrote ${DROPIN#"$ROOT"}"
fi

mkdir -p "$JOURNAL_DIR"
say "restarting systemd-journald"
systemctl restart systemd-journald || die "systemctl restart systemd-journald failed"
journalctl --flush || die "journalctl --flush failed"

for _ in 1 2 3 4 5; do
  journal_active && break
  sleep 1
done
journal_active || die "journald restarted but ${JOURNAL_DIR#"$ROOT"}/<machine-id>/system.journal did not appear"

after=$(effective_storage)
[[ "$after" == "persistent" ]] || die "effective Storage is ${after} after applying"

say "done: the journal is now written to ${JOURNAL_DIR#"$ROOT"} and will survive power loss"
echo "    after the next reboot, 'journalctl --list-boots' will show the previous boot"
