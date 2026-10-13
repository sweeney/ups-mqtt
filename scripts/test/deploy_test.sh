#!/usr/bin/env bash
# Tests for deploy.sh's argument handling and --install-unit, with ssh, scp
# and go stubbed out.  Nothing is built or contacted.
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
TMPDIR="$WORK/tmp" run --install-unit pi@host
check "install-unit: staging removed"   '[[ -z "$(ls -A "$WORK/tmp")" ]]'
check "install-unit: exit 0"            '[[ $CODE == 0 ]]'
check "install-unit: ships, then ssh -t" '[[ $CALLS == "$SCP_CALL"$'"'"'\n'"'"'"$SSH_CALL" ]]'
check "install-unit: builds nothing"    '[[ $CALLS != *"== go"* ]]'
check "install-unit: ships all three"   '[[ "$(ls "$WORK/shipped" | tr "\n" " ")" == "install-sudoers.sh install-unit.sh ups-mqtt.service " ]]'
check "  …the repo's unit, unchanged"    'cmp -s "$WORK/shipped/ups-mqtt.service" "$REPO/ups-mqtt.service"'

run pi@host --install-unit
check "install-unit: host first too"    '[[ $CODE == 0 && $CALLS == "$SCP_CALL"$'"'"'\n'"'"'"$SSH_CALL" ]]'

run --install-unit
check "install-unit: default host"      '[[ $CODE == 0 && $CALLS == *$'"'"'-t\nsweeney@garibaldi\n'"'"'* ]]'

STUB_SSH_EXIT=1 run --install-unit pi@host
check "install-unit: passes on an abort" '[[ $CODE == 1 ]]'

STUB_SCP_FAIL=1 run --install-unit pi@host
check "install-unit: scp fails, no ssh" '[[ $CODE != 0 && $CALLS != *"== ssh"* ]]'

# ── --install-sudoers: the same, running the sudoers installer ────────────
run --install-sudoers pi@host
check "install-sudoers: ships, ssh -t"  '[[ $CODE == 0 && $CALLS == "$SCP_CALL"$'"'"'\n== ssh\n-t\npi@host\nsudo bash ~/ups-mqtt/setup/install-sudoers.sh'"'"' ]]'

run --install-sudoers --install-unit pi@host
check "two installs: exit 2, no calls"  '[[ $CODE == 2 && $OUT == *"one --install-* at a time"* && -z $CALLS ]]'

# ── what deploy.sh runs as root must match the narrow sudoers rule ─────────
# Anything else would need a password, which an unattended ssh cannot give.
# A whole deploy runs against a stub host (no config yet, so the config tee
# runs too), then again with the service failing to start, and every sudo in
# the commands they sent is checked against the rule install-sudoers.sh
# writes, paths included.
FULL="$WORK/full"
mkdir -p "$FULL/scripts" "$FULL/bin"
cp "$REPO/deploy.sh" "$REPO/ups-mqtt.service" "$FULL/"
cp "$REPO/scripts/install-unit.sh" "$REPO/scripts/install-sudoers.sh" "$FULL/scripts/"
echo '# test config' > "$FULL/config.toml"
cat > "$FULL/bin/ssh" <<EOF
#!/usr/bin/env bash
cmd=\${@: -1}
printf '%s\n==\n' "\$cmd" >> "$WORK/remote"
case "\$cmd" in
  "uname -m")                       echo x86_64 ;;
  "[ -L "*)                         exit 0 ;;
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
OUT=$(cd "$FULL" && PATH="$FULL/bin:$PATH" bash ./deploy.sh pi@host 2>&1 < /dev/null)
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
OUT=$(cd "$FULL" && STUB_STATUS=failed PATH="$FULL/bin:$PATH" bash ./deploy.sh pi@host 2>&1 < /dev/null)
CODE=$?
set -e
check "failed start: reported"          '[[ $CODE == 1 && $OUT == *"Service is '"'"'failed'"'"'"* ]]'
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
run a@host b@host
check "two hosts: exit 2, no calls"     '[[ $CODE == 2 && $OUT == *"more than one host"* && -z $CALLS ]]'

echo "deploy: $PASS passed, $FAIL failed"
[[ $FAIL == 0 ]]
