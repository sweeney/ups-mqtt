#!/usr/bin/env bash
# Tests for scripts/persist-journal.sh, run against fixture trees with
# systemctl and journalctl stubbed out.  No root, no real journald.
#
#   bash scripts/test/persist-journal_test.sh
set -euo pipefail

HERE=$(cd "$(dirname "$0")" && pwd)
SCRIPT="$HERE/../persist-journal.sh"
MACHINE_ID="0123456789abcdef0123456789abcdef"

PASS=0
FAIL=0
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

# new_fixture builds a Raspberry Pi OS-like tree: journald.conf with Storage
# commented out, and the RPi drop-in forcing volatile storage.
new_fixture() {
  local root="$WORK/$1"
  rm -rf "$root"
  mkdir -p "$root/etc/systemd" "$root/usr/lib/systemd/journald.conf.d" "$root/var/log/journal" "$root/bin"
  printf '[Journal]\n#Storage=auto\n' > "$root/etc/systemd/journald.conf"
  printf '[Journal]\nStorage=volatile\n' > "$root/usr/lib/systemd/journald.conf.d/40-rpi-volatile-storage.conf"
  printf '[Journal]\nForwardToSyslog=yes\n' > "$root/usr/lib/systemd/journald.conf.d/syslog.conf"
  echo "$MACHINE_ID" > "$root/etc/machine-id"

  # systemctl stub: records the call; restarting journald creates the
  # persistent journal file, as the real one does once Storage=persistent —
  # unless STUB_JOURNALD_BROKEN is set.
  cat > "$root/bin/systemctl" <<EOF
#!/usr/bin/env bash
echo "systemctl \$*" >> "$root/calls"
if [[ "\$*" == "restart systemd-journald" && -z "\${STUB_JOURNALD_BROKEN:-}" ]]; then
  mkdir -p "$root/var/log/journal/$MACHINE_ID"
  : > "$root/var/log/journal/$MACHINE_ID/system.journal"
fi
EOF
  printf '#!/usr/bin/env bash\necho "journalctl $*" >> "%s/calls"\n' "$root" > "$root/bin/journalctl"
  # sleep stub so the failure path does not wait 5 s.
  printf '#!/usr/bin/env bash\n:\n' > "$root/bin/sleep"
  chmod +x "$root/bin/"*
  : > "$root/calls"
  echo "$root"
}

# run executes the script against a fixture; sets OUT and CODE.
run() {
  local root=$1; shift
  set +e
  OUT=$(PATH="$root/bin:$PATH" PERSIST_JOURNAL_ROOT="$root" bash "$SCRIPT" "$@" 2>&1 < "${STDIN:-/dev/null}")
  CODE=$?
  set -e
}

check() {
  local name=$1 cond=$2
  if eval "$cond"; then
    PASS=$((PASS + 1))
  else
    FAIL=$((FAIL + 1))
    echo "FAIL: $name"
    echo "      condition: $cond"
    echo "      exit: $CODE"
    echo "$OUT" | sed 's/^/      | /'
  fi
}

DROPIN_REL="etc/systemd/journald.conf.d/60-persistent-storage.conf"

# ── --check on a stock RPi tree reports, changes nothing, exits 1 ──────────
r=$(new_fixture check_stock)
run "$r" --check
check "check: exit 1"                 '[[ $CODE == 1 ]]'
check "check: sees volatile"          '[[ $OUT == *"effective Storage=volatile"* ]]'
check "check: says what it would do"  '[[ $OUT == *"Would:"* && $OUT == *"restart systemd-journald"* ]]'
check "check: wrote nothing"          '[[ ! -e $r/$DROPIN_REL ]]'
check "check: ran no commands"        '[[ ! -s $r/calls ]]'

# ── apply with --yes ───────────────────────────────────────────────────────
r=$(new_fixture apply)
run "$r" --yes
check "apply: exit 0"                 '[[ $CODE == 0 ]]'
check "apply: drop-in written"        'grep -qx "Storage=persistent" "$r/$DROPIN_REL"'
check "apply: sync interval set"      'grep -qx "SyncIntervalSec=10s" "$r/$DROPIN_REL"'
check "apply: journald restarted"     'grep -qx "systemctl restart systemd-journald" "$r/calls"'
check "apply: journal flushed"        'grep -qx "journalctl --flush" "$r/calls"'
check "apply: restart before flush"   '[[ $(head -1 "$r/calls") == "systemctl restart systemd-journald" ]]'
check "apply: RPi drop-in untouched"  'grep -qx "Storage=volatile" "$r/usr/lib/systemd/journald.conf.d/40-rpi-volatile-storage.conf"'
check "apply: reports done"           '[[ $OUT == *"done:"* ]]'

# ── re-running is a no-op ──────────────────────────────────────────────────
: > "$r/calls"
run "$r" --yes
check "rerun: exit 0"                 '[[ $CODE == 0 ]]'
check "rerun: nothing to do"          '[[ $OUT == *"already persistent"* ]]'
check "rerun: no restart"             '[[ ! -s $r/calls ]]'
run "$r" --check
check "check after apply: exit 0"     '[[ $CODE == 0 ]]'

# ── the prompt: anything but yes aborts without changes ────────────────────
r=$(new_fixture prompt_no)
echo n > "$WORK/no"
STDIN="$WORK/no" run "$r"
check "prompt no: exit 1"             '[[ $CODE == 1 ]]'
check "prompt no: aborted"            '[[ $OUT == *"Aborted; nothing changed."* ]]'
check "prompt no: wrote nothing"      '[[ ! -e $r/$DROPIN_REL ]]'
check "prompt no: no restart"         '[[ ! -s $r/calls ]]'

r=$(new_fixture prompt_eof)
run "$r"
check "prompt EOF: aborts"            '[[ $CODE == 1 && ! -e $r/$DROPIN_REL ]]'

r=$(new_fixture prompt_yes)
echo y > "$WORK/yes"
STDIN="$WORK/yes" run "$r"
check "prompt yes: applies"           '[[ $CODE == 0 && -e $r/$DROPIN_REL ]]'

# ── an out-of-date drop-in is rewritten ────────────────────────────────────
r=$(new_fixture stale_dropin)
mkdir -p "$(dirname "$r/$DROPIN_REL")"
printf '[Journal]\nStorage=persistent\n' > "$r/$DROPIN_REL"
run "$r" --yes
check "stale drop-in: exit 0"         '[[ $CODE == 0 ]]'
check "stale drop-in: rewritten"      'grep -qx "SyncIntervalSec=10s" "$r/$DROPIN_REL"'

# ── drop-in right but journal not yet on disk: restart only ────────────────
r=$(new_fixture not_active)
run "$r" --yes
rm -rf "$r/var/log/journal/$MACHINE_ID"
: > "$r/calls"
run "$r" --yes
check "not active: exit 0"            '[[ $CODE == 0 ]]'
check "not active: restarted"         'grep -qx "systemctl restart systemd-journald" "$r/calls"'

# ── a later drop-in that would override ours: refuse before changing ───────
r=$(new_fixture overridden)
mkdir -p "$r/etc/systemd/journald.conf.d"
printf '[Journal]\nStorage=volatile\n' > "$r/etc/systemd/journald.conf.d/90-volatile.conf"
run "$r" --yes
check "overridden: exit 2"            '[[ $CODE == 2 ]]'
check "overridden: explains"          '[[ $OUT == *"Storage=volatile"* && $OUT == *"would not take effect"* ]]'
check "overridden: wrote nothing"     '[[ ! -e $r/$DROPIN_REL ]]'
check "overridden: no restart"        '[[ ! -s $r/calls ]]'

# ── /etc masks /usr/lib by filename, as systemd does ───────────────────────
r=$(new_fixture masked)
mkdir -p "$r/etc/systemd/journald.conf.d"
printf '[Journal]\nStorage=persistent\n' > "$r/etc/systemd/journald.conf.d/40-rpi-volatile-storage.conf"
run "$r" --check
check "masking: /etc wins"            '[[ $OUT == *"effective Storage=persistent"* ]]'

# ── journald fails to start writing: error, not success ────────────────────
r=$(new_fixture broken)
STUB_JOURNALD_BROKEN=1 run "$r" --yes
check "broken journald: exit 2"       '[[ $CODE == 2 ]]'
check "broken journald: explains"     '[[ $OUT == *"did not appear"* ]]'

# ── preconditions and arguments ────────────────────────────────────────────
r=$(new_fixture no_machine_id)
rm "$r/etc/machine-id"
run "$r" --check
check "no machine-id: exit 2"         '[[ $CODE == 2 && $OUT == *"machine-id"* ]]'

r=$(new_fixture bad_arg)
run "$r" --frobnicate
check "unknown arg: exit 2"           '[[ $CODE == 2 && $OUT == *"unknown argument"* ]]'
run "$r" --help
check "help: exit 0 with usage"       '[[ $CODE == 0 && $OUT == *"Usage"* ]]'

echo "persist-journal: $PASS passed, $FAIL failed"
[[ $FAIL == 0 ]]
