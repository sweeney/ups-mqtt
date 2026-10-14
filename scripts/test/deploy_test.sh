#!/usr/bin/env bash
# Tests for deploy.sh's argument handling (--site, --host),
# --install-unit and the read-only --check, with ssh, scp and go stubbed
# out.  Nothing is built or contacted.
#
#   bash scripts/test/deploy_test.sh
set -euo pipefail

HERE=$(cd "$(dirname "$0")" && pwd)
REPO=$(cd "$HERE/../.." && pwd)

PASS=0
FAIL=0
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

# Each stub writes its arguments one per line, then a separator, to calls;
# STUB_SCP_FAIL and STUB_SSH_EXIT make scp fail or ssh exit with that code.
mkdir -p "$WORK/bin"
for tool in ssh scp go; do
  cat > "$WORK/bin/$tool" <<EOF
#!/usr/bin/env bash
if [[ $tool == scp ]]; then
  rm -rf -- "$WORK/shipped"
  for a in "\$@"; do [[ -d "\$a" && "\$a" == */setup ]] && cp -R "\$a" "$WORK/shipped"; done
fi
{ echo "== $tool"; for a in "\$@"; do [[ "\$a" == */setup ]] && a=SETUP; printf '%s\n' "\$a"; done; } >> "$WORK/calls"
[[ $tool != scp || -z "\${STUB_SCP_FAIL:-}" ]] || exit 1
[[ $tool != ssh ]] || exit "\${STUB_SSH_EXIT:-0}"
EOF
done
chmod +x "$WORK/bin/"*

# The tests' own sites file; a checkout's real one is never read.
cat > "$WORK/sites" <<EOF
# site  ssh
a       user@a-host
b       user@b-host   # a comment
EOF
export UPS_MQTT_SITES="$WORK/sites"

# run executes deploy.sh from the repo root; sets OUT, CODE and CALLS.
run() {
  : > "$WORK/calls"
  set +e
  OUT=$(cd "$REPO" && PATH="$WORK/bin:$PATH" bash ./deploy.sh "$@" 2>&1 < /dev/null)
  CODE=$?
  set -e
  CALLS=$(cat "$WORK/calls")
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
    echo "$CALLS" | sed 's/^/      calls| /'
  fi
}

# The remote command must reach ssh as one argument, so ~ is expanded on the
# host: unquoted, the local shell turned it into this machine's home.
# scp gets one directory, setup/, holding the unit and both installers; the
# stub records it as SETUP and copies its contents to $WORK/shipped.
SCP_CALL=$'== scp\n-q\n-r\nSETUP\npi@host:~/ups-mqtt/'
SSH_CALL=$'== ssh\n-t\npi@host\nsudo bash ~/ups-mqtt/setup/install-unit.sh'

# ── --install-unit: ship, then run the installer with a terminal ───────────
mkdir -p "$WORK/tmp"
TMPDIR="$WORK/tmp" run --install-unit --host pi@host
check "install-unit: staging removed"   '[[ -z "$(ls -A "$WORK/tmp")" ]]'
check "install-unit: exit 0"            '[[ $CODE == 0 ]]'
check "install-unit: ships, then ssh -t" '[[ $CALLS == "$SCP_CALL"$'"'"'\n'"'"'"$SSH_CALL" ]]'
check "install-unit: builds nothing"    '[[ $CALLS != *"== go"* ]]'
check "install-unit: ships all three"   '[[ "$(ls "$WORK/shipped" | tr "\n" " ")" == "install-sudoers.sh install-unit.sh ups-mqtt.service " ]]'
check "  …the repo's unit, unchanged"    'cmp -s "$WORK/shipped/ups-mqtt.service" "$REPO/ups-mqtt.service"'

run --host pi@host --install-unit
check "install-unit: host first too"    '[[ $CODE == 0 && $CALLS == "$SCP_CALL"$'"'"'\n'"'"'"$SSH_CALL" ]]'

run --install-unit --site b
check "install-unit: by site"           '[[ $CODE == 0 && $CALLS == *$'"'"'-t\nuser@b-host\n'"'"'* ]]'

STUB_SSH_EXIT=1 run --install-unit --host pi@host
check "install-unit: passes on an abort" '[[ $CODE == 1 ]]'

STUB_SCP_FAIL=1 run --install-unit --host pi@host
check "install-unit: scp fails, no ssh" '[[ $CODE != 0 && $CALLS != *"== ssh"* ]]'

# ── --install-sudoers: the same, running the sudoers installer ────────────
run --install-sudoers --host pi@host
check "install-sudoers: ships, ssh -t"  '[[ $CODE == 0 && $CALLS == "$SCP_CALL"$'"'"'\n== ssh\n-t\npi@host\nsudo bash ~/ups-mqtt/setup/install-sudoers.sh'"'"' ]]'

run --install-sudoers --install-unit --host pi@host
check "two installs: exit 2, no calls"  '[[ $CODE == 2 && $OUT == *"one --install-* at a time"* && -z $CALLS ]]'

# ── what deploy.sh runs as root must match the narrow sudoers rule ─────────
# Anything else would need a password, which an unattended ssh cannot give.
# A whole deploy runs against a stub host (no config yet, so the config tee
# runs too), then again with the service failing to start, and every sudo in
# the commands they sent is checked against the rule install-sudoers.sh
# writes, paths included.
FULL="$WORK/full"
mkdir -p "$FULL/scripts" "$FULL/bin"
cp "$REPO/deploy.sh" "$REPO/deploy-sites.sh" "$REPO/ups-mqtt.service" "$FULL/"
cp "$REPO/scripts/install-unit.sh" "$REPO/scripts/install-sudoers.sh" "$FULL/scripts/"
echo '# test config' > "$FULL/config.toml"
cat > "$FULL/bin/ssh" <<EOF
#!/usr/bin/env bash
cmd=\${@: -1}
printf '%s\n==\n' "\$cmd" >> "$WORK/remote"
case "\$cmd" in
  "uname -m")                       echo x86_64 ;;
  "[ -L "*)                         exit "\${STUB_NO_LINK:-0}" ;;
  *"install-unit.sh --check"*)      exit "\${STUB_UNIT_DIFFERS:-0}" ;;
  "[ -f "*)                         exit 1 ;;
  *"systemctl is-active"*)          echo "\${STUB_STATUS:-active}" ;;
esac
exit 0
EOF
printf '#!/usr/bin/env bash\nprintf "%%s\\n" "$@" >> "%s"\n' "$WORK/fullscp" > "$FULL/bin/scp"
printf '#!/usr/bin/env bash\nexit 0\n' > "$FULL/bin/sleep"
printf '#!/usr/bin/env bash\nwhile [[ $# -gt 0 ]]; do [[ $1 == -o ]] && : > "$2"; shift; done\n' > "$FULL/bin/go"
chmod +x "$FULL/bin/"*
: > "$WORK/remote"
set +e
OUT=$(cd "$FULL" && PATH="$FULL/bin:$PATH" bash ./deploy.sh --host pi@host 2>&1 < /dev/null)
CODE=$?
set -e
CALLS=$(cat "$WORK/remote")
check "full deploy: completes"          '[[ $CODE == 0 && $OUT == *"Deployed"* ]]'
check "full deploy: reached the tee"    '[[ $CALLS == *"sudo tee /etc/ups-mqtt/config.toml"* ]]'
# Hosts with the installed unit symlinked to ~/ups-mqtt/ups-mqtt.service had
# it overwritten by the repo's unit; nothing may be copied there again.
# The unit may only travel inside the staged setup/ directory: copied on its
# own, to ~/ups-mqtt/ or to the file there, it lands on the old path.
check "full deploy: unit only in setup/" '[[ -s $WORK/fullscp ]] && ! grep -qE "(^|/|:)ups-mqtt\.service\$" "$WORK/fullscp" && grep -qE "/setup\$" "$WORK/fullscp"'
set +e
OUT=$(cd "$FULL" && STUB_STATUS=failed PATH="$FULL/bin:$PATH" bash ./deploy.sh --host pi@host 2>&1 < /dev/null)
CODE=$?
set -e
check "failed start: reported"          '[[ $CODE == 1 && $OUT == *"Service is '"'"'failed'"'"'"* ]]'
# The hints name the host as it was given, so they can be pasted back.
set +e
OUT=$(cd "$FULL" && STUB_NO_LINK=1 PATH="$FULL/bin:$PATH" bash ./deploy.sh --host pi@host 2>&1 < /dev/null)
CODE=$?
set -e
check "new host: hint names it"         '[[ $CODE == 1 && $OUT == *"./first-install.sh --host pi@host first"* ]]'
set +e
OUT=$(cd "$FULL" && STUB_UNIT_DIFFERS=1 PATH="$FULL/bin:$PATH" bash ./deploy.sh --host pi@host 2>&1 < /dev/null)
CODE=$?
set -e
check "unit differs: hint names it"     '[[ $CODE == 0 && $OUT == *"./deploy.sh --install-unit --host pi@host"* ]]'
set +e
OUT=$(cd "$FULL" && STUB_UNIT_DIFFERS=1 PATH="$FULL/bin:$PATH" bash ./deploy.sh --site a 2>&1 < /dev/null)
CODE=$?
set -e
check "  …or the site"                  '[[ $CODE == 0 && $OUT == *"./deploy.sh --install-unit --site a"* ]]'
CALLS=$(cat "$WORK/remote")

RULE_CMDS=$(sed -n 's/^\${USER_NAME} ALL=(root) NOPASSWD: //p' "$REPO/scripts/install-sudoers.sh" \
  | sed 's/\${SYSTEMCTL}/\/usr\/bin\/systemctl/g; s/\${TEE}/\/usr\/bin\/tee/g; s/\${SERVICE}/ups-mqtt/g; s/\${CONFIG}/\/etc\/ups-mqtt\/config.toml/g' \
  | tr ',' '\n' | sed 's/^ *//; s/ *$//')
# Every sudo, with any options, up to the end of its shell command; a bare
# command name is what sudo finds on its secure_path, /usr/bin on both hosts.
DEPLOY_CMDS=$(grep -oE 'sudo( +-[^ ]+)* +[^"&;|>]+' <<<"$CALLS" \
  | sed -E 's/^sudo +//; s/ +$//; s/^([a-z][^ \/]*)/\/usr\/bin\/\1/' | sort -u)
check "sudo: rule parsed"               '[[ $(wc -l <<<"$RULE_CMDS") -eq 3 ]]'
check "sudo: deploy runs three"         '[[ $(wc -l <<<"$DEPLOY_CMDS") -eq 3 ]]'
while read -r c; do
  check "sudo: rule allows '$c'"        'grep -qxF -- "$c" <<<"$RULE_CMDS"'
done <<<"$DEPLOY_CMDS"

# ── arguments ──────────────────────────────────────────────────────────────
run --frobnicate
check "unknown arg: exit 2, no calls"   '[[ $CODE == 2 && $OUT == *"unknown argument"* && -z $CALLS ]]'
run a@host
check "bare host: exit 2, no calls"     '[[ $CODE == 2 && $OUT == *"not '"'"'a@host'"'"'"* && -z $CALLS ]]'

# ── choosing the host: --site, --host, and no default ─────────────────────
run
check "no host: exit 2, no calls"       '[[ $CODE == 2 && $OUT == *"name the host"* && -z $CALLS ]]'
check "  …lists the sites"              '[[ $OUT == *"(sites: a b)"* ]]'
run --site c
check "unknown site: exit 2, no calls"  '[[ $CODE == 2 && $OUT == *"site '"'"'c'"'"' is not in"* && -z $CALLS ]]'
run --site
check "--site, no name: exit 2"         '[[ $CODE == 2 && $OUT == *"--site needs a name"* && -z $CALLS ]]'
run --host
check "--host, no target: exit 2"       '[[ $CODE == 2 && $OUT == *"--host needs USER@HOST"* && -z $CALLS ]]'
run --install-unit --site a --host pi@host
check "--host wins over --site"         '[[ $CODE == 0 && $CALLS == *$'"'"'-t\npi@host\n'"'"'* && $CALLS != *a-host* ]]'
UPS_MQTT_SITES="$WORK/none" run --install-unit --site a
check "no sites file: exit 2, no calls" '[[ $CODE == 2 && $OUT == *"copy sites.example to sites"* && -z $CALLS ]]'
UPS_MQTT_SITES="$WORK/none" run --install-unit --host pi@host
check "  …--host needs none"            '[[ $CODE == 0 && $CALLS == *$'"'"'-t\npi@host\n'"'"'* ]]'
UPS_MQTT_SITES="$REPO/sites.example" run --install-unit --site home
check "sites.example parses"            '[[ $CODE == 0 && $CALLS == *$'"'"'-t\nadmin@100.64.0.20\n'"'"'* ]]'
for args in "--host --site" "--site --host" "--host -oProxyCommand=x"; do
  run $args
  check "$args: exit 2, no calls"       '[[ $CODE == 2 && $OUT == *"needs"* && -z $CALLS ]]'
done
run --site a --site b
check "--site twice: exit 2, no calls"  '[[ $CODE == 2 && $OUT == *"--site given twice"* && -z $CALLS ]]'
run --host x@y --host pi@host
check "--host twice: exit 2, no calls"  '[[ $CODE == 2 && $OUT == *"--host given twice"* && -z $CALLS ]]'
printf '# site ssh\n#c user@c-host\nd user@d-host\r\ne   # no host yet\n' > "$WORK/sites-odd"
UPS_MQTT_SITES="$WORK/sites-odd" run --install-unit --site "#c"
check "commented site: not found"       '[[ $CODE == 2 && $OUT == *"not in"* && -z $CALLS ]]'
UPS_MQTT_SITES="$WORK/sites-odd" run --install-unit --site "#"
check "header line: not a site"         '[[ $CODE == 2 && -z $CALLS ]]'
UPS_MQTT_SITES="$WORK/sites-odd" run --install-unit --site d
check "CRLF sites file: no \\r"          '[[ $CODE == 0 && $CALLS == *$'"'"'-t\nuser@d-host\n'"'"'* ]]'
UPS_MQTT_SITES="$WORK/sites-odd" run --install-unit --site e
check "site with no host: not found"    '[[ $CODE == 2 && $OUT == *"not in"* && -z $CALLS ]]'
UPS_MQTT_SITES="$WORK/sites-odd" run
check "  …nor listed"                   '[[ $OUT == *"(sites: d)"* ]]'
: > "$WORK/calls"
set +e
OUT=$(cd / && PATH="$WORK/bin:$PATH" bash "$REPO/deploy.sh" --install-unit --site a 2>&1 < /dev/null)
CODE=$?
set -e
CALLS=$(cat "$WORK/calls")
check "run from elsewhere: works"       '[[ $CODE == 0 && $CALLS == *$'"'"'-t\nuser@a-host\n'"'"'* ]]'
OUT=$(cd / && bash "$REPO/deploy.sh" --help 2>&1)
check "  …and --help"                    '[[ $OUT == *"--site NAME"* ]]'
run --help
check "--help: usage, no calls"         '[[ $CODE == 0 && $OUT == *"--site NAME"* && -z $CALLS ]]'

# ── --check: report, change nothing ───────────────────────────────────────
# A stub host answers the state probe from $WORK/probe and the unit check
# with STUB_UNIT_RC, saving the tar stream it was sent.  Every remote
# command is logged; none may change anything.
CHK="$WORK/chk"
mkdir -p "$CHK/scripts" "$CHK/bin"
cp "$REPO/deploy.sh" "$REPO/deploy-sites.sh" "$REPO/ups-mqtt.service" "$CHK/"
cp "$REPO/scripts/install-unit.sh" "$REPO/scripts/install-sudoers.sh" "$CHK/scripts/"
echo '# test config' > "$CHK/config.toml"
cat > "$CHK/bin/ssh" <<EOF
#!/usr/bin/env bash
cmd=\${@: -1}
printf '%s\n==\n' "\$cmd" >> "$WORK/chkremote"
case "\$cmd" in
  *"echo arch"*)        [[ -z "\${STUB_PROBE_FAIL:-}" ]] || exit 255; cat "$WORK/probe" ;;
  *"install-unit.sh"*)  cat > "$WORK/chktar"; echo "unit check ran"; exit "\${STUB_UNIT_RC:-0}" ;;
esac
exit 0
EOF
for tool in scp go; do printf '#!/usr/bin/env bash\necho "%s $*" >> "%s"\n' "$tool" "$WORK/chkremote" > "$CHK/bin/$tool"; done
chmod +x "$CHK/bin/"*

# A host ready to deploy: the narrow rule, plus a password-protected ALL.
GOOD_PROBE='arch aarch64
link /home/u/ups-mqtt/ups-mqtt
current /home/u/ups-mqtt/ups-mqtt-20261014-210455
versions 3
config present
active active
sudoers User u may run the following commands on host:
sudoers     (ALL : ALL) ALL
sudoers     (root) NOPASSWD: /usr/bin/systemctl daemon-reload, /usr/bin/systemctl restart ups-mqtt, /usr/bin/tee /etc/ups-mqtt/config.toml'

# chk PROBE [ARGS…]: run deploy.sh --check against the stub; sets OUT, CODE, CALLS.
chk() {
  printf '%s\n' "$1" > "$WORK/probe"; shift
  : > "$WORK/chkremote"; rm -f "$WORK/chktar"
  set +e
  OUT=$(cd "$CHK" && PATH="$CHK/bin:$PATH" bash ./deploy.sh --check --host pi@host "$@" 2>&1 < /dev/null)
  CODE=$?
  set -e
  CALLS=$(cat "$WORK/chkremote")
}
# What a read-only check may never do: build, copy, or run anything as root
# except listing its own sudo rights.
read_only() {
  [[ $CALLS != *"go "* && $CALLS != *"scp "* ]] &&
    ! grep -E 'sudo' <<<"$CALLS" | grep -vq 'sudo -n -l 2>/dev/null' &&
    [[ $CALLS != *"ln -sf"* && $CALLS != *"systemctl restart"* && $CALLS != *"rm --"* ]]
}

chk "$GOOD_PROBE"
check "check: ready, exit 0"            '[[ $CODE == 0 && $OUT == *"Ready to deploy: ./deploy.sh --host pi@host"* ]]'
check "  …changes nothing"              'read_only'
check "  …reports the build"            '[[ $OUT == *"deployed:  ups-mqtt-20261014-210455"* && $OUT == *"prunes 1"* ]]'
check "  …unit checked from a temp dir" '[[ $OUT == *"unit check ran"* && $CALLS == *"mktemp -d"*"--check"* && $CALLS != *"~/ups-mqtt/setup"* ]]'
check "  …with this checkout's unit"   '[[ "$(tar -tf "$WORK/chktar" | sort | tr "\n" " ")" == "install-unit.sh ups-mqtt.service " ]]'
check "  …and its installer"            'tar -xOf "$WORK/chktar" install-unit.sh | cmp -s - "$REPO/scripts/install-unit.sh"'

# The trap the first version fell into: (ALL : ALL) ALL allows every
# command, but only with a password, which a deploy cannot give.
chk "$(grep -v NOPASSWD <<<"$GOOD_PROBE")"
check "password-only sudo: exit 1"      '[[ $CODE == 1 && $(grep -c "sudo would ask for a password" <<<"$OUT") == 3 ]]'
check "  …says how to fix it"           '[[ $OUT == *"./deploy.sh --install-sudoers --host pi@host"* ]] && read_only'
chk "$(sed 's|, /usr/bin/systemctl restart ups-mqtt||' <<<"$GOOD_PROBE")"
check "one command missing: exit 1"     '[[ $CODE == 1 && $OUT == *"password for: systemctl restart ups-mqtt"* && $(grep -c "would ask" <<<"$OUT") == 1 ]]'
chk "$(sed 's|NOPASSWD: .*|NOPASSWD: /usr/bin/systemctl restart ups-mqttX, /usr/bin/tee /etc/ups-mqtt/configXtoml|' <<<"$GOOD_PROBE")"
check "near misses don't count"        '[[ $CODE == 1 && $(grep -c "would ask" <<<"$OUT") == 3 ]]'
chk "$(sed 's|NOPASSWD: .*|NOPASSWD: ALL|' <<<"$GOOD_PROBE")"
check "NOPASSWD: ALL: exit 0"           '[[ $CODE == 0 ]]'
chk "$(sed 's|/usr/bin/|/bin/|g' <<<"$GOOD_PROBE")"
check "other command paths: exit 0"     '[[ $CODE == 0 ]]'

chk "$(sed 's|^link .*|link none|' <<<"$GOOD_PROBE")"
check "not installed: exit 1"           '[[ $CODE == 1 && $OUT == *"./first-install.sh --host pi@host first"* ]] && read_only'
chk "$(sed 's|^active .*|active failed|' <<<"$GOOD_PROBE")"
check "service down: exit 1"            '[[ $CODE == 1 && $OUT == *"service is '"'"'failed'"'"'"* ]]'
chk "$(sed 's|^arch .*|arch mips|' <<<"$GOOD_PROBE")"
check "unsupported arch: exit 1"        '[[ $CODE == 1 && $OUT == *"unsupported architecture: mips"* ]]'
chk "$(sed 's|^config .*|config absent|' <<<"$GOOD_PROBE")"
check "no host config: would install"   '[[ $CODE == 0 && $OUT == *"config.toml would be installed"* ]] && read_only'
mv "$CHK/config.toml" "$CHK/config.toml.away"
chk "$(sed 's|^config .*|config absent|' <<<"$GOOD_PROBE")"
mv "$CHK/config.toml.away" "$CHK/config.toml"
check "  …and none here: exit 1"        '[[ $CODE == 1 && $OUT == *"the service would not start"* ]]'

STUB_UNIT_RC=1 chk "$GOOD_PROBE"
check "unit differs: exit 1"            '[[ $CODE == 1 && $OUT == *"./deploy.sh --install-unit --host pi@host"* ]] && read_only'
STUB_UNIT_RC=2 chk "$GOOD_PROBE"
check "unit check fails: exit 1"        '[[ $CODE == 1 && $OUT == *"install-unit.sh --check failed (exit 2)"* ]]'
STUB_PROBE_FAIL=1 chk "$GOOD_PROBE"
check "host unreachable: exit 2"        '[[ $CODE == 2 && $OUT == *"cannot read"* && $CALLS != *"install-unit"* ]]'

# A check that goes wrong must stop, never fall through into the deploy:
# bash abandons the whole if-block on an arithmetic error, set -e or not.
chk "$(sed 's|^versions .*|versions 1 2|' <<<"$GOOD_PROBE")"
check "garbled probe: exit 2"           '[[ $CODE == 2 && $OUT == *"unreadable version count"* && $OUT != *"Detecting architecture"* ]] && read_only'
chk "$(sed 's|^versions .*|versions a[$(touch '"$WORK"'/pwned)]|' <<<"$GOOD_PROBE")"
check "host text never evaluated"       '[[ $CODE == 2 && ! -e $WORK/pwned ]] && read_only'
chk "$(sed 's|^active .*|active|' <<<"$GOOD_PROBE")"
check "empty field is empty"            '[[ $CODE == 1 && $OUT == *"service is '"''"'"* ]]'

# Tags, runas, and where the command lives.
chk "$(sed 's|NOPASSWD: |NOPASSWD: SETENV: |' <<<"$GOOD_PROBE")"
check "SETENV: tag: still passwordless" '[[ $CODE == 0 ]]'
chk "$(sed 's|(root) NOPASSWD: .*|(root) NOPASSWD: /usr/bin/systemctl daemon-reload, PASSWD: /usr/bin/systemctl restart ups-mqtt, NOPASSWD: /usr/bin/tee /etc/ups-mqtt/config.toml|' <<<"$GOOD_PROBE")"
check "PASSWD: in between: just that"   '[[ $CODE == 1 && $OUT == *"password for: systemctl restart ups-mqtt"* && $(grep -c "would ask" <<<"$OUT") == 1 ]]'
chk "$(sed 's|(root) NOPASSWD: .*|(postgres) NOPASSWD: ALL|' <<<"$GOOD_PROBE")"
check "other runas: does not count"     '[[ $CODE == 1 && $(grep -c "would ask" <<<"$OUT") == 3 ]]'
chk "$(sed 's|(root) NOPASSWD: .*|(ALL : ALL) NOPASSWD: ALL|' <<<"$GOOD_PROBE")"
check "runas ALL: counts"               '[[ $CODE == 0 ]]'
chk "$(sed 's|(root) NOPASSWD: .*|(ALL, !root) NOPASSWD: ALL|' <<<"$GOOD_PROBE")"
check "runas excluding root: no"        '[[ $CODE == 1 && $(grep -c "would ask" <<<"$OUT") == 3 ]]'
chk "$(sed 's|(root) NOPASSWD: .*|(root) NOPASSWD: ALL, !/usr/bin/systemctl restart ups-mqtt|' <<<"$GOOD_PROBE")"
check "ALL minus a command: no"         '[[ $CODE == 1 && $(grep -c "would ask" <<<"$OUT") == 3 ]]'
chk "$(sed 's|/usr/bin/systemctl|/home/u/bin/systemctl|g' <<<"$GOOD_PROBE")"
check "user-writable path: no"          '[[ $CODE == 1 && $(grep -c "would ask" <<<"$OUT") == 2 ]]'

# Exactly two remote commands, the probe and the unit check; nothing else.
chk "$GOOD_PROBE"
check "exactly the two remote reads"    '[[ $(grep -cx "==" <<<"$CALLS") == 2 && $(sed -n 2p <<<"$CALLS") == *"echo arch"* && $CALLS == *$'"'"'==\nd=$(mktemp -d) && trap'"'"'* ]]'
check "  …the unit sent is this one"    'tar -xOf "$WORK/chktar" ups-mqtt.service | cmp -s - "$REPO/ups-mqtt.service"'
mkdir -p "$WORK/chktmp"
printf '%s\n' "$GOOD_PROBE" > "$WORK/probe"
OUT=$(cd "$CHK" && TMPDIR="$WORK/chktmp" PATH="$CHK/bin:$PATH" bash ./deploy.sh --dry-run --host pi@host 2>&1 < /dev/null) || true
check "--dry-run: same as --check"      '[[ $OUT == *"Ready to deploy"* ]]'
check "  …staging removed"              '[[ -z "$(ls -A "$WORK/chktmp")" ]]'

run --check --install-unit --host pi@host
check "--check with an install: exit 2" '[[ $CODE == 2 && $OUT == *"--check reports on a deploy"* && -z $CALLS ]]'
run --check
check "--check, no host: exit 2"        '[[ $CODE == 2 && $OUT == *"name the host"* && -z $CALLS ]]'

echo "deploy: $PASS passed, $FAIL failed"
[[ $FAIL == 0 ]]
