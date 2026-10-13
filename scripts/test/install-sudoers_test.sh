#!/usr/bin/env bash
# Tests for scripts/install-sudoers.sh, run against fixture trees with
# visudo, sudo and id stubbed out.  No root, no real sudoers.
#
#   bash scripts/test/install-sudoers_test.sh
set -euo pipefail

HERE=$(cd "$(dirname "$0")" && pwd)
SCRIPT="$HERE/../install-sudoers.sh"

PASS=0
FAIL=0
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

# OLD_RULE is what remote-setup.sh used to install: root-equivalent.
OLD_RULE='alice ALL=(ALL) NOPASSWD: /usr/bin/systemctl, /usr/bin/journalctl, /usr/bin/tee /etc/ups-mqtt/config.toml'
NEW_LINE='alice ALL=(root) NOPASSWD: /usr/bin/systemctl daemon-reload, /usr/bin/systemctl restart ups-mqtt, /usr/bin/tee /etc/ups-mqtt/config.toml'

# new_fixture builds a tree with OLD_RULE installed and stubs for:
#   visudo  `-c -f FILE` passes unless STUB_VISUDO_FILE_FAIL; plain `-c`
#           (the whole config) fails when STUB_VISUDO_ALL_FAIL is "before"
#           (every time) or "after" (from the second check on); each plain
#           check also records what sudo would read from sudoers.d then
#   chmod   records what sudo would read from sudoers.d, then chmods
#   sudo    `-l -U USER` prints $STUB_SUDO_LIST
#   id      knows alice only; `id -un` is the real one
# and installs a unit as first-install.sh does: owned by whoever runs the
# tests (the script expects that under a fixture root), User=alice.
new_fixture() {
  local root="$WORK/$1"
  rm -rf "$root"
  mkdir -p "$root/etc/sudoers.d" "$root/usr/bin" "$root/bin"
  printf '%s\n' "$OLD_RULE" > "$root/etc/sudoers.d/ups-mqtt"
  chmod 440 "$root/etc/sudoers.d/ups-mqtt"
  printf '#!/bin/sh\n' > "$root/usr/bin/systemctl"
  printf '#!/bin/sh\n' > "$root/usr/bin/tee"
  mkdir -p "$root/etc/systemd/system"
  printf '[Service]\nUser=alice\nExecStart=/usr/local/bin/ups-mqtt\n' > "$root/etc/systemd/system/ups-mqtt.service"
  chmod 644 "$root/etc/systemd/system/ups-mqtt.service"
  cat > "$root/bin/visudo" <<EOF
#!/usr/bin/env bash
echo "visudo \$*" >> "$root/visudo-calls"
if [[ "\$*" == "-c -f "* ]]; then
  [[ -z "\${STUB_VISUDO_FILE_FAIL:-}" ]] || { echo "\$3:1: syntax error" >&2; exit 1; }
else
  ls -A "$root/etc/sudoers.d" | grep -v '[.~]' | tr '\n' ' ' >> "$root/sudo-reads"; echo >> "$root/sudo-reads"
  n=\$(grep -cx "visudo -c" "$root/visudo-calls")
  case "\${STUB_VISUDO_ALL_FAIL:-}" in
    before)          echo "/etc/sudoers.d/other: syntax error" >&2; exit 1 ;;
    after) [[ \$n -lt 2 ]] || { echo "/etc/sudoers.d/other: syntax error" >&2; exit 1; } ;;
  esac
fi
EOF
  cat > "$root/bin/chmod" <<EOF
#!/usr/bin/env bash
ls -A "$root/etc/sudoers.d" | grep -v '[.~]' | tr '\n' ' ' >> "$root/sudo-reads"; echo >> "$root/sudo-reads"
exec /bin/chmod "\$@"
EOF
  cat > "$root/bin/sudo" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "${STUB_SUDO_LIST:-}"
EOF
  cat > "$root/bin/id" <<'EOF'
#!/usr/bin/env bash
[[ "$1" != -un ]] || exec /usr/bin/id -un
[[ "$1" == alice ]]
EOF
  chmod +x "$root/usr/bin/"* "$root/bin/"*
  echo "$root"
}

# run executes the script against a fixture as alice's sudo; sets OUT, CODE.
run() {
  local root=$1; shift
  set +e
  OUT=$(PATH="$root/bin:$PATH" SUDO_USER="${AS-alice}" INSTALL_SUDOERS_ROOT="$root" \
    bash "$SCRIPT" "$@" 2>&1 < "${STDIN:-/dev/null}")
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

RULE_REL="etc/sudoers.d/ups-mqtt"
rule_line() { grep -v '^#' "$1/$RULE_REL"; }
untouched() { [[ "$(cat "$1/$RULE_REL")" == "$OLD_RULE" ]]; }

# ── --check shows the diff, changes nothing, exits 1 ──────────────────────
r=$(new_fixture check)
run "$r" --check
check "check: exit 1"                  '[[ $CODE == 1 && $OUT == *"Would:"* ]]'
check "check: diff old to new"         '[[ $OUT == *"-$OLD_RULE"* && $OUT == *"+$NEW_LINE"* ]]'
check "check: rule verified"           'grep -q -- "-c -f" "$r/visudo-calls"'
check "check: rule untouched"          'untouched "$r"'

# ── apply with --yes ───────────────────────────────────────────────────────
r=$(new_fixture apply)
run "$r" --yes
check "apply: exit 0"                  '[[ $CODE == 0 ]]'
check "apply: the narrow rule only"    '[[ "$(rule_line "$r")" == "$NEW_LINE" ]]'
check "apply: no bare systemctl"       '! rule_line "$r" | grep -qE "systemctl(,|$)"'
check "apply: no journalctl"           '[[ "$(rule_line "$r")" != *journalctl* ]]'
check "apply: mode 440"                '[[ $(ls -l "$r/$RULE_REL" | cut -c1-10) == "-r--r-----" ]]'
check "apply: staged file gone"        '[[ -z "$(ls -A "$r/etc/sudoers.d" | grep "^\.")" ]]'
check "apply: sudo never sees staging" '[[ -s $r/sudo-reads ]] && ! grep -qv "^ups-mqtt $" "$r/sudo-reads"'
check "apply: whole config checked"    'grep -qx "visudo -c" "$r/visudo-calls"'
check "apply: nothing broad left"      '[[ $OUT == *"no passwordless ALL"* ]]'

# ── re-running is a no-op ──────────────────────────────────────────────────
: > "$r/visudo-calls"
run "$r" --yes
check "rerun: up to date"              '[[ $CODE == 0 && $OUT == *"up to date"* && ! -s $r/visudo-calls ]]'
run "$r" --check
check "check after apply: exit 0"      '[[ $CODE == 0 ]]'

# ── first install: no rule yet ─────────────────────────────────────────────
r=$(new_fixture fresh)
rm "$r/$RULE_REL"
run "$r" --yes
check "fresh: installs"                '[[ $CODE == 0 && "$(rule_line "$r")" == "$NEW_LINE" ]]'

# ── --user overrides SUDO_USER ─────────────────────────────────────────────
r=$(new_fixture as_root)
AS="" run "$r" --yes --user alice
check "--user: installs for alice"     '[[ $CODE == 0 && "$(rule_line "$r")" == "$NEW_LINE" ]]'

# ── the prompt ─────────────────────────────────────────────────────────────
r=$(new_fixture prompt_no)
echo n > "$WORK/no"
STDIN="$WORK/no" INSTALL_SUDOERS_TTY=1 run "$r"
check "prompt no: aborted, untouched"  '[[ $CODE == 1 && $OUT == *"Aborted"* ]] && untouched "$r"'

r=$(new_fixture prompt_yes)
echo y > "$WORK/yes"
STDIN="$WORK/yes" INSTALL_SUDOERS_TTY=1 run "$r"
check "prompt yes: installs"           '[[ $CODE == 0 && "$(rule_line "$r")" == "$NEW_LINE" ]]'

r=$(new_fixture no_tty)
STDIN="$WORK/yes" run "$r"
check "no tty: refuses, says ssh -t"   '[[ $CODE == 2 && $OUT == *"ssh -t"* && $OUT != *"About to"* ]] && untouched "$r"'

# ── visudo failures ────────────────────────────────────────────────────────
r=$(new_fixture rule_bad)
STUB_VISUDO_FILE_FAIL=1 run "$r" --yes
check "rule rejected: exit 2"          '[[ $CODE == 2 && $OUT == *"visudo rejects"* ]] && untouched "$r"'

r=$(new_fixture already_bad)
STUB_VISUDO_ALL_FAIL=before run "$r" --yes
check "config already bad: refused"    '[[ $CODE == 2 && $OUT == *"already fails"* && $OUT != *"About to"* ]] && untouched "$r"'

r=$(new_fixture whole_bad)
STUB_VISUDO_ALL_FAIL=after run "$r" --yes
check "config fails after: restored"   '[[ $CODE == 2 && $OUT == *"previous rule is restored"* ]] && untouched "$r"'
check "  …mode kept"                   '[[ $(ls -l "$r/$RULE_REL" | cut -c1-10) == "-r--r-----" ]]'

r=$(new_fixture whole_bad_fresh)
rm "$r/$RULE_REL"
STUB_VISUDO_ALL_FAIL=after run "$r" --yes
check "config fails, fresh: removed"   '[[ $CODE == 2 && ! -e $r/$RULE_REL ]]'

# ── other grants that keep the user root are reported ─────────────────────
# broad_report runs an install with $1 added to the `sudo -l` listing.
broad_report() {
  local r
  BROAD_N=$((BROAD_N + 1))
  r=$(new_fixture "others_$BROAD_N")
  STUB_SUDO_LIST="User alice may run the following commands on host:
    (ALL : ALL) ALL
    (root) NOPASSWD: /usr/bin/systemctl daemon-reload, /usr/bin/systemctl restart ups-mqtt
$1" run "$r" --yes
}
BROAD_N=0
reported()     { [[ $CODE == 0 && $OUT == *"still run these"* && $OUT == *"       $1"* ]]; }
not_reported() { [[ $CODE == 0 && $OUT != *"still run these"* && $OUT == *"no passwordless ALL"* ]]; }

broad_report "    (ALL) NOPASSWD: /usr/bin/systemctl, /usr/bin/journalctl"
check "others: bare systemctl"         'reported "/usr/bin/systemctl (root)"'
check "others: bare journalctl"        'reported "/usr/bin/journalctl (read or delete any log)"'
broad_report "    (ALL) NOPASSWD: ALL"
check "others: NOPASSWD ALL"           'reported "ALL (root)"'
broad_report "    (ALL) NOPASSWD: SETENV: /usr/bin/systemctl"
check "others: tag after NOPASSWD"     'reported "/usr/bin/systemctl (root)"'
broad_report "    (ALL) NOPASSWD: /usr/bin/systemctl *"
check "others: wildcard args"          'reported "/usr/bin/systemctl * (root)"'
broad_report "    (ALL) NOPASSWD: /bin/systemctl, /bin/journalctl"
check "others: other path"             'reported "/bin/systemctl (root)" && reported "/bin/journalctl"'
broad_report "    (ALL) NOPASSWD: /usr/bin/systemctl restart pi-helper, /usr/bin/journalctl -u pi-helper *"
check "others: arg wildcard reported"  'reported "/usr/bin/journalctl -u pi-helper * (read"'
check "  …exact args not"              '[[ $OUT != *"restart pi-helper"* ]]'
broad_report "    (ALL) NOPASSWD: /usr/bin/true, PASSWD: /usr/bin/systemctl, NOPASSWD: /usr/bin/journalctl"
check "others: tags switch per entry"  'reported "/usr/bin/journalctl" && [[ $OUT != *"       /usr/bin/systemctl"* ]]'
broad_report "    (ALL) NOPASSWD: /usr/bin/true, (bob) /usr/bin/systemctl"
check "others: runas mid-list"         'reported "/usr/bin/systemctl (root)"'
broad_report "    (ALL) /usr/bin/systemctl, /usr/bin/journalctl"
check "others: with password: fine"    'not_reported'
broad_report ""
check "others: narrow only: fine"      'not_reported'

# ── the unit must not be changeable without root ───────────────────────────
UNIT_REL="etc/systemd/system/ups-mqtt.service"
r=$(new_fixture unit_ok)
run "$r" --check
check "unit ok: no warning"            '[[ $OUT != *"through the unit"* ]]'
r=$(new_fixture unit_symlink)
mv "${r:?}/$UNIT_REL" "${r:?}/home-unit"; ln -s "${r:?}/home-unit" "${r:?}/$UNIT_REL"
run "$r" --check
check "unit symlink: warns"            '[[ $OUT == *"through the unit"* && $OUT == *"is a symlink"* ]]'
r=$(new_fixture unit_writable)
chmod 666 "${r:?}/$UNIT_REL"
run "$r" --check
check "unit writable: warns"           '[[ $OUT == *"through the unit"* && $OUT == *"writable"* ]]'
r=$(new_fixture unit_root_user)
printf '[Service]\nUser=root\n' > "${r:?}/$UNIT_REL"
run "$r" --check
check "unit User=root: warns"          '[[ $OUT == *"runs as root"* ]]'
r=$(new_fixture unit_no_user)
printf '[Service]\nExecStart=/x\n' > "${r:?}/$UNIT_REL"
run "$r" --check
check "unit no User=: warns"           '[[ $OUT == *"no User="* ]]'
r=$(new_fixture unit_dropin)
mkdir -p "${r:?}/$UNIT_REL.d"
printf '[Service]\n' > "${r:?}/$UNIT_REL.d/ok.conf"; printf '[Service]\n' > "${r:?}/$UNIT_REL.d/bad.conf"
chmod 644 "${r:?}/$UNIT_REL.d/ok.conf"; chmod 666 "${r:?}/$UNIT_REL.d/bad.conf"
run "$r" --check
check "unit drop-in: names bad one"    '[[ $OUT == *"bad.conf"* && $OUT != *"ok.conf"* ]]'
r=$(new_fixture unit_absent)
mv "${r:?}/$UNIT_REL" "${r:?}/moved-away"
run "$r" --check
check "unit absent: no warning"        '[[ $CODE == 1 && $OUT != *"through the unit"* ]]'

# ── preconditions ──────────────────────────────────────────────────────────
r=$(new_fixture pre)
AS="" run "$r" --yes
check "no user: exit 2"                '[[ $CODE == 2 && $OUT == *"--user"* ]] && untouched "$r"'
AS=root run "$r" --yes
check "root: refused"                  '[[ $CODE == 2 && $OUT == *"for root"* ]] && untouched "$r"'
AS=bob run "$r" --yes
check "unknown user: exit 2"           '[[ $CODE == 2 && $OUT == *"no such user"* ]] && untouched "$r"'
AS='alice ALL=(ALL) NOPASSWD: ALL #' run "$r" --yes
check "injected name: refused"         '[[ $CODE == 2 && $OUT == *"not a plain user name"* ]] && untouched "$r"'
rm "$r/usr/bin/systemctl"
run "$r" --yes
check "no systemctl: exit 2"           '[[ $CODE == 2 && $OUT == *"/usr/bin/systemctl not found"* ]] && untouched "$r"'

# ── real root is required without a fixture root ───────────────────────────
# Skipped as root: it would then act on this machine's real /etc/sudoers.d.
if [[ $(/usr/bin/id -u) -ne 0 ]]; then
  set +e
  OUT=$(SUDO_USER=alice bash "$SCRIPT" --check 2>&1 < /dev/null); CODE=$?
  set -e
  check "not root: refused"            '[[ $CODE == 2 && $OUT == *"needs root"* ]]'
fi

# ── arguments ──────────────────────────────────────────────────────────────
r=$(new_fixture args)
run "$r" --frobnicate
check "unknown arg: exit 2"            '[[ $CODE == 2 && $OUT == *"unknown argument"* ]]'
run "$r" --user
check "--user without name: exit 2"    '[[ $CODE == 2 && $OUT == *"needs a name"* ]]'
run "$r" --help
check "help: exit 0 with usage"        '[[ $CODE == 0 && $OUT == *"Usage"* ]]'

echo "install-sudoers: $PASS passed, $FAIL failed"
[[ $FAIL == 0 ]]
