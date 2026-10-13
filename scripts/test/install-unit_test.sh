#!/usr/bin/env bash
# Tests for scripts/install-unit.sh, run against fixture trees with
# systemctl and systemd-analyze stubbed out.  No root, no real systemd.
#
#   bash scripts/test/install-unit_test.sh
set -euo pipefail

HERE=$(cd "$(dirname "$0")" && pwd)
SCRIPT="$HERE/../install-unit.sh"
REPO_UNIT="$HERE/../../ups-mqtt.service"

PASS=0
FAIL=0
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

# OLD_UNIT is a unit as first-install.sh left it, before the change being
# delivered: rendered for user alice.
OLD_UNIT='[Unit]
Description=UPS MQTT Bridge (NUT)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=alice
ExecStart=/usr/local/bin/ups-mqtt --config /etc/ups-mqtt/config.toml
Restart=on-failure

[Install]
WantedBy=multi-user.target'

# new_fixture builds a tree with OLD_UNIT installed, and stubs for systemctl
# (records calls) and systemd-analyze (passes unless STUB_VERIFY_FAIL is set).
new_fixture() {
  local root="$WORK/$1"
  rm -rf "$root"
  mkdir -p "$root/etc/systemd/system" "$root/bin"
  printf '%s\n' "$OLD_UNIT" > "$root/etc/systemd/system/ups-mqtt.service"
  printf '#!/usr/bin/env bash\necho "systemctl $*" >> "%s/calls"\n' "$root" > "$root/bin/systemctl"
  cat > "$root/bin/systemd-analyze" <<EOF
#!/usr/bin/env bash
echo "systemd-analyze \$*" >> "$root/verified"
if [[ -n "\${STUB_VERIFY_FAIL:-}" ]]; then
  echo "ups-mqtt.service: Unknown key name 'Bogus' in section 'Unit'" >&2
  exit 1
fi
EOF
  chmod +x "$root/bin/"*
  : > "$root/calls"
  echo "$root"
}

# run executes the script against a fixture; sets OUT and CODE.
run() {
  local root=$1; shift
  set +e
  OUT=$(PATH="$root/bin:$PATH" INSTALL_UNIT_ROOT="$root" bash "$SCRIPT" "$@" 2>&1 < "${STDIN:-/dev/null}")
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

UNIT_REL="etc/systemd/system/ups-mqtt.service"
# expected is the repo unit as it should be installed for alice.
expected() { sed 's/^User=SERVICE_USER$/User=alice/' "$REPO_UNIT"; }

# ── --check shows the diff, changes nothing, exits 1 ──────────────────────
r=$(new_fixture check)
run "$r" --check "$REPO_UNIT"
check "check: exit 1"                  '[[ $CODE == 1 ]]'
check "check: shows a unified diff"    '[[ $OUT == *"--- installed /etc/systemd/system/ups-mqtt.service"* && $OUT == *"+++ new"* ]]'
check "check: diff has the new line"   '[[ $OUT == *"+After=network-online.target tailscaled.service mosquitto.service"* ]]'
check "check: keeps the user"          '[[ $OUT == *"(User=alice)"* && $OUT != *"-User=alice"* ]]'
check "check: verifies the new unit"   '[[ -s $r/verified ]]'
check "check: says what it would do"   '[[ $OUT == *"Would:"* ]]'
check "check: unit untouched"          '[[ "$(cat "$r/$UNIT_REL")" == "$OLD_UNIT" ]]'
check "check: no systemctl calls"      '[[ ! -s $r/calls ]]'

# ── apply with --yes ───────────────────────────────────────────────────────
r=$(new_fixture apply)
run "$r" --yes "$REPO_UNIT"
check "apply: exit 0"                  '[[ $CODE == 0 ]]'
check "apply: installed = rendered"    '[[ "$(cat "$r/$UNIT_REL")" == "$(expected)" ]]'
check "apply: placeholder filled"      '! grep -q SERVICE_USER "$r/$UNIT_REL"'
check "apply: mode 644"                '[[ $(ls -l "$r/$UNIT_REL" | cut -c1-10) == "-rw-r--r--" ]]'
check "apply: no temp file left"       '[[ ! -e $r/$UNIT_REL.tmp ]]'
check "apply: reload then try-restart" '[[ "$(cat "$r/calls")" == $'"'"'systemctl daemon-reload\nsystemctl try-restart ups-mqtt'"'"' ]]'
check "apply: reports done"            '[[ $OUT == *"done:"* ]]'

# ── re-running is a no-op ──────────────────────────────────────────────────
: > "$r/calls"
run "$r" --yes "$REPO_UNIT"
check "rerun: exit 0"                  '[[ $CODE == 0 && $OUT == *"up to date"* ]]'
check "rerun: no systemctl calls"      '[[ ! -s $r/calls ]]'
run "$r" --check "$REPO_UNIT"
check "check after apply: exit 0"      '[[ $CODE == 0 ]]'

# ── the prompt: anything but yes aborts without changes ────────────────────
r=$(new_fixture prompt_no)
echo n > "$WORK/no"
STDIN="$WORK/no" INSTALL_UNIT_TTY=1 run "$r" "$REPO_UNIT"
check "prompt no: exit 1, aborted"     '[[ $CODE == 1 && $OUT == *"Aborted; nothing changed."* ]]'
check "prompt no: unit untouched"      '[[ "$(cat "$r/$UNIT_REL")" == "$OLD_UNIT" ]]'
check "prompt no: no systemctl calls"  '[[ ! -s $r/calls ]]'

r=$(new_fixture prompt_eof)
INSTALL_UNIT_TTY=1 run "$r" "$REPO_UNIT"
check "prompt EOF: aborts"             '[[ $CODE == 1 && "$(cat "$r/$UNIT_REL")" == "$OLD_UNIT" ]]'

r=$(new_fixture prompt_yes)
echo y > "$WORK/yes"
STDIN="$WORK/yes" INSTALL_UNIT_TTY=1 run "$r" "$REPO_UNIT"
check "prompt yes: installs"           '[[ $CODE == 0 && "$(cat "$r/$UNIT_REL")" == "$(expected)" ]]'

# ── no terminal (ssh without -t): refuse up front, even if stdin says yes ──
r=$(new_fixture no_tty)
STDIN="$WORK/yes" run "$r" "$REPO_UNIT"
check "no tty: exit 2, says ssh -t"    '[[ $CODE == 2 && $OUT == *"ssh -t"* && $OUT == *"nothing changed"* ]]'
check "no tty: no prompt shown"        '[[ $OUT != *"About to:"* ]]'
check "no tty: unit untouched"         '[[ "$(cat "$r/$UNIT_REL")" == "$OLD_UNIT" ]]'
check "no tty: no systemctl calls"     '[[ ! -s $r/calls ]]'

# ── default source: the repo layout (script in scripts/, unit at the root) ─
r=$(new_fixture default_source)
run "$r" --yes
check "default source: repo unit"      '[[ $CODE == 0 && "$(cat "$r/$UNIT_REL")" == "$(expected)" ]]'

# ── default source: the host layout (both shipped to one directory) ────────
r=$(new_fixture host_layout)
mkdir -p "$r/home"
cp "$SCRIPT" "$r/home/install-unit.sh"
sed 's/^Description=.*/Description=from the host copy/' "$REPO_UNIT" > "$r/home/ups-mqtt.service"
set +e
OUT=$(PATH="$r/bin:$PATH" INSTALL_UNIT_ROOT="$r" bash "$r/home/install-unit.sh" --yes 2>&1 < /dev/null)
CODE=$?
set -e
check "host layout: uses sibling unit" '[[ $CODE == 0 ]] && grep -qx "Description=from the host copy" "$r/$UNIT_REL"'

# ── an installed symlink is replaced by a file; its target is left alone ───
r=$(new_fixture symlink)
mv "$r/$UNIT_REL" "$r/target.service"
ln -s "$r/target.service" "$r/$UNIT_REL"
run "$r" --yes "$REPO_UNIT"
check "symlink: exit 0"                '[[ $CODE == 0 ]]'
check "symlink: now a regular file"    '[[ -f $r/$UNIT_REL && ! -L $r/$UNIT_REL ]]'
check "symlink: target untouched"      '[[ "$(cat "$r/target.service")" == "$OLD_UNIT" ]]'

# ── systemd-analyze rejects the unit: error, nothing changed ───────────────
r=$(new_fixture verify_fails)
STUB_VERIFY_FAIL=1 run "$r" --yes "$REPO_UNIT"
check "verify fails: exit 2"           '[[ $CODE == 2 && $OUT == *"rejects the new unit"* ]]'
check "verify fails: shows why"        '[[ $OUT == *"Unknown key name"* ]]'
check "verify fails: unit untouched"   '[[ "$(cat "$r/$UNIT_REL")" == "$OLD_UNIT" ]]'
check "verify fails: no systemctl"     '[[ ! -s $r/calls ]]'

# ── preconditions ──────────────────────────────────────────────────────────
r=$(new_fixture not_installed)
rm "$r/$UNIT_REL"
run "$r" --check "$REPO_UNIT"
check "not installed: exit 2"          '[[ $CODE == 2 && $OUT == *"first-install.sh"* ]]'

r=$(new_fixture no_user)
grep -v '^User=' <<<"$OLD_UNIT" > "$r/$UNIT_REL"
run "$r" --yes "$REPO_UNIT"
check "no User=: exit 2"               '[[ $CODE == 2 && $OUT == *"no User= line"* ]]'

r=$(new_fixture placeholder_installed)
sed 's/^User=alice$/User=SERVICE_USER/' <<<"$OLD_UNIT" > "$r/$UNIT_REL"
run "$r" --yes "$REPO_UNIT"
check "placeholder installed: exit 2"  '[[ $CODE == 2 && $OUT == *"placeholder"* ]]'

r=$(new_fixture no_placeholder)
grep -v '^User=' "$REPO_UNIT" > "$WORK/no_placeholder.service"
run "$r" --yes "$WORK/no_placeholder.service"
check "source lacks placeholder"       '[[ $CODE == 2 && $OUT == *"no '"'"'User=SERVICE_USER'"'"' line"* ]]'
check "  …and nothing changed"         '[[ "$(cat "$r/$UNIT_REL")" == "$OLD_UNIT" ]]'

r=$(new_fixture missing_source)
run "$r" --check "$WORK/nope.service"
check "missing source: exit 2"         '[[ $CODE == 2 && $OUT == *"not found"* ]]'

# ── arguments ──────────────────────────────────────────────────────────────
r=$(new_fixture args)
run "$r" --frobnicate
check "unknown arg: exit 2"            '[[ $CODE == 2 && $OUT == *"unknown argument"* ]]'
run "$r" a.service b.service
check "two sources: exit 2"            '[[ $CODE == 2 && $OUT == *"more than one"* ]]'
run "$r" --help
check "help: exit 0 with usage"        '[[ $CODE == 0 && $OUT == *"Usage"* ]]'

echo "install-unit: $PASS passed, $FAIL failed"
[[ $FAIL == 0 ]]
