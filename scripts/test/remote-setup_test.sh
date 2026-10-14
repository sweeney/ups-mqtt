#!/usr/bin/env bash
# Tests for remote-setup.sh, run against fixture trees with getent,
# systemctl and the sudoers installer stubbed out.  No root, no real
# systemd: REMOTE_SETUP_ROOT points the system paths into the fixture.
#
#   bash scripts/test/remote-setup_test.sh
set -euo pipefail

HERE=$(cd "$(dirname "$0")" && pwd)
REPO=$(cd "$HERE/../.." && pwd)

PASS=0
FAIL=0
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

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

# new_fixture NAME: a host as first-install.sh leaves it for user alice —
# binary linked, unit and installers in setup/, a config staged — with
# empty /etc and /usr/local/bin.  Prints the fixture's root.
new_fixture() {
  local root="$WORK/$1"
  rm -rf "$root"
  local home="$root/home/alice"
  mkdir -p "$home/ups-mqtt/setup" "$root/etc/systemd/system" "$root/usr/local/bin" "$root/bin"
  : > "$home/ups-mqtt/ups-mqtt-20261014-120000"
  chmod +x "$home/ups-mqtt/ups-mqtt-20261014-120000"
  ln -s "$home/ups-mqtt/ups-mqtt-20261014-120000" "$home/ups-mqtt/ups-mqtt"
  cp "$REPO/ups-mqtt.service" "$home/ups-mqtt/setup/"
  cp "$REPO/remote-setup.sh" "$home/ups-mqtt/"
  echo '# staged config' > "$home/ups-mqtt/config.toml.pending"
  # The sudoers installer: --check (a read) says the rule is missing, or
  # STUB_SUDOERS_CHECK; an install is logged and exits STUB_SUDOERS_EXIT.
  cat > "$home/ups-mqtt/setup/install-sudoers.sh" <<EOF
#!/usr/bin/env bash
if [[ \$1 == --check ]]; then echo "\$*" > "$root/checkargs"; echo "rule: not installed yet"; exit "\${STUB_SUDOERS_CHECK:-1}"; fi
echo "install-sudoers \$*" >> "$root/calls"
exit "\${STUB_SUDOERS_EXIT:-0}"
EOF
  printf '#!/usr/bin/env bash\necho "systemctl $*" >> "%s/calls"\n' "$root" > "$root/bin/systemctl"
  # getent passwd USER: alice lives in the fixture; anyone else is unknown.
  printf '#!/usr/bin/env bash\n[[ $2 == alice ]] && echo "alice:x:1000:1000::%s:/bin/bash"\n' "$home" > "$root/bin/getent"
  # logname: no login name, so an unset SUDO_USER leaves nobody to guess.
  printf '#!/usr/bin/env bash\nexit 1\n' > "$root/bin/logname"
  chmod +x "$root/bin/"*
  : > "$root/calls"
  echo "$root"
}

# run ROOT [ARGS…]: runs the fixture's copy as SUDO_USER (default alice);
# sets OUT, CODE and CALLS.
run() {
  local root=$1; shift
  set +e
  OUT=$(PATH="$root/bin:$PATH" REMOTE_SETUP_ROOT="$root" SUDO_USER="${AS-alice}" \
    bash "$root/home/alice/ups-mqtt/remote-setup.sh" "$@" 2>&1 < /dev/null)
  CODE=$?
  set -e
  CALLS=$(cat "$root/calls")
}

# ── a first install ────────────────────────────────────────────────────────
R=$(new_fixture fresh)
run "$R"
check "install: exit 0"                 '[[ $CODE == 0 && $OUT == *"Service enabled and started"* ]]'
check "  …config installed"             '[[ "$(cat "$R/etc/ups-mqtt/config.toml")" == "# staged config" && ! -e "$R/home/alice/ups-mqtt/config.toml.pending" ]]'
check "  …binary linked"                '[[ "$(readlink "$R/usr/local/bin/ups-mqtt")" == "$R/home/alice/ups-mqtt/ups-mqtt" ]]'
check "  …unit for alice"               'grep -qx "User=alice" "$R/etc/systemd/system/ups-mqtt.service" && ! grep -q SERVICE_USER "$R/etc/systemd/system/ups-mqtt.service"'
check "  …unit otherwise the repo's"     'diff <(grep -v "^User=" "$REPO/ups-mqtt.service") <(grep -v "^User=" "$R/etc/systemd/system/ups-mqtt.service") >/dev/null'
check "  …unit a file, mode 644"        '[[ -f "$R/etc/systemd/system/ups-mqtt.service" && ! -L "$R/etc/systemd/system/ups-mqtt.service" && -n "$(find "$R/etc/systemd/system/ups-mqtt.service" -maxdepth 0 -perm 644)" ]]'
check "  …no temp file left"            '[[ ! -e "$R/etc/systemd/system/ups-mqtt.service.tmp" ]]'
check "  …sudoers for alice, then start" '[[ $CALLS == $'"'"'install-sudoers --yes --user alice\nsystemctl daemon-reload\nsystemctl enable ups-mqtt\nsystemctl start ups-mqtt'"'"' ]]'
check "  …sudoers checked for alice first" '[[ "$(cat "$R/checkargs")" == "--check --user alice" ]]'

# ── re-running is safe ─────────────────────────────────────────────────────
echo '# edited on the host' > "$R/etc/ups-mqtt/config.toml"
: > "$R/calls"
run "$R"
check "re-run: exit 0"                  '[[ $CODE == 0 ]]'
check "  …keeps the host's config"       '[[ "$(cat "$R/etc/ups-mqtt/config.toml")" == "# edited on the host" ]]'
check "  …same link and unit"           '[[ "$(readlink "$R/usr/local/bin/ups-mqtt")" == "$R/home/alice/ups-mqtt/ups-mqtt" ]] && grep -qx "User=alice" "$R/etc/systemd/system/ups-mqtt.service"'

# An early install left the unit symlinked into the user's home: the new
# unit must replace the link, not write through it.
R=$(new_fixture symlinked)
echo 'old unit' > "$R/home/alice/ups-mqtt/ups-mqtt.service"
ln -s "$R/home/alice/ups-mqtt/ups-mqtt.service" "$R/etc/systemd/system/ups-mqtt.service"
run "$R"
check "symlinked unit: replaced"        '[[ $CODE == 0 && ! -L "$R/etc/systemd/system/ups-mqtt.service" ]] && grep -qx "User=alice" "$R/etc/systemd/system/ups-mqtt.service"'
check "  …user's copy untouched"         '[[ "$(cat "$R/home/alice/ups-mqtt/ups-mqtt.service")" == "old unit" ]]'

# ── no config anywhere: warns, still installs ─────────────────────────────
R=$(new_fixture noconfig)
rm "$R/home/alice/ups-mqtt/config.toml.pending"
run "$R"
check "no config: warns"                '[[ $CODE == 0 && $OUT == *"No config at /etc/ups-mqtt/config.toml"* ]]'

# untouched ROOT: nothing installed or run, the staged config still staged.
untouched() {
  [[ -z "$(find "$1/etc/systemd/system" "$1/usr/local/bin" -mindepth 1)" && ! -e "$1/etc/ups-mqtt" && -z $CALLS &&
     -e "$1/home/alice/ups-mqtt/config.toml.pending" ]]
}
# ── --check: plans, changes nothing ───────────────────────────────────────
R=$(new_fixture check)
run "$R" --check
check "check: exit 0, a plan"           '[[ $CODE == 0 && $OUT == *"would install the staged config"* && $OUT == *"User=alice"* ]]'
check "  …nothing written"              'untouched "$R"'

# ── refusals: before anything is written ──────────────────────────────────
R=$(new_fixture asroot)
AS=root run "$R"
check "as root: refused"                '[[ $CODE == 2 && $OUT == *"not as root"* ]] && untouched "$R"'
R=$(new_fixture nouser)
AS="" run "$R"
check "no user: refused, no guess"      '[[ $CODE == 2 && $OUT == *"cannot tell which user"* ]] && untouched "$R"'
R=$(new_fixture stranger)
AS=bob run "$R"
check "unknown user: refused"           '[[ $CODE == 2 && $OUT == *"no home directory for bob"* ]] && untouched "$R"'
R=$(new_fixture nounit)
rm "$R/home/alice/ups-mqtt/setup/ups-mqtt.service"
run "$R"
check "no unit shipped: refused"        '[[ $CODE == 2 && $OUT == *"setup/ups-mqtt.service not found"* ]] && untouched "$R"'
R=$(new_fixture rendered)
sed -i.bak 's/^User=SERVICE_USER$/User=carol/' "$R/home/alice/ups-mqtt/setup/ups-mqtt.service"
run "$R"
check "no placeholder: refused"         '[[ $CODE == 2 && $OUT == *"no '"'"'User=SERVICE_USER'"'"' line"* ]] && untouched "$R"'
R=$(new_fixture nosudoers)
rm "$R/home/alice/ups-mqtt/setup/install-sudoers.sh"
run "$R"
check "no sudoers installer: refused"   '[[ $CODE == 2 && $OUT == *"install-sudoers.sh not found"* ]] && untouched "$R"'
R=$(new_fixture nobinary)
rm "$R/home/alice/ups-mqtt/ups-mqtt"
run "$R"
check "no binary: refused"              '[[ $CODE == 2 && $OUT == *"run first-install.sh first"* ]] && untouched "$R"'
R=$(new_fixture badarg)
run "$R" --frobnicate
check "unknown arg: exit 2"             '[[ $CODE == 2 && $OUT == *"unknown argument"* ]] && untouched "$R"'

# The sudoers installer's own preconditions are checked before any write.
R=$(new_fixture novisudo)
STUB_SUDOERS_CHECK=2 run "$R"
check "sudoers cannot install: refused" '[[ $CODE == 2 && $OUT == *"sudoers rule cannot be installed"* ]] && untouched "$R"'

# --check says when a staged config would replace an installed one.
R=$(new_fixture replace)
mkdir -p "$R/etc/ups-mqtt"; echo '# installed' > "$R/etc/ups-mqtt/config.toml"
run "$R" --check
check "check: warns of a replace"       '[[ $CODE == 0 && $OUT == *"would REPLACE /etc/ups-mqtt/config.toml"* && $OUT == *"rule: not installed yet"* ]]'

# A binary link that points nowhere is refused.
R=$(new_fixture dangling)
rm "$R/home/alice/ups-mqtt/ups-mqtt-20261014-120000"
run "$R"
check "dangling binary link: refused"   '[[ $CODE == 2 && $OUT == *"broken link"* ]] && untouched "$R"'

# A failing sudoers install stops it before the service is started.
R=$(new_fixture sudoersfails)
STUB_SUDOERS_EXIT=1 run "$R"
check "sudoers fails: not started"      '[[ $CODE != 0 && $CALLS != *"systemctl start"* ]]'

# Without a test root it insists on root.  Run as root this would be a
# real install, so it is skipped then.
if [[ $EUID -ne 0 ]]; then
  set +e
  OUT=$(env -u REMOTE_SETUP_ROOT -u SUDO_USER bash "$REPO/remote-setup.sh" 2>&1 < /dev/null); CODE=$?
  set -e
  check "not root: refused"             '[[ $CODE == 2 && $OUT == *"run this with sudo"* ]]'
else
  echo "skip: the not-root check, since this runs as root"
fi

echo "remote-setup: $PASS passed, $FAIL failed"
[[ $FAIL == 0 ]]
