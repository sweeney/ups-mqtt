#!/usr/bin/env bash
# Tests for first-install.sh's choice of host (--site, --host, no default),
# with ssh, scp and go stubbed out.  Nothing is built or contacted.
#
#   bash scripts/test/first-install_test.sh
set -euo pipefail

HERE=$(cd "$(dirname "$0")" && pwd)
REPO=$(cd "$HERE/../.." && pwd)

PASS=0
FAIL=0
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

# Each stub records its arguments, one per line, after a header line.  ssh
# answers uname and reports no config yet; go writes the -o file.
mkdir -p "$WORK/bin"
for tool in ssh scp; do
  cat > "$WORK/bin/$tool" <<EOF
#!/usr/bin/env bash
{ echo "== $tool"; printf '%s\n' "\$@"; } >> "$WORK/calls"
[[ $tool == ssh && "\${@: -1}" == "uname -m" ]] && echo x86_64
[[ $tool == ssh && "\${@: -1}" == *"config.toml ]"* ]] && echo no
exit 0
EOF
done
printf '#!/usr/bin/env bash\necho "== go" >> "%s"\nwhile [[ $# -gt 0 ]]; do [[ $1 == -o ]] && : > "$2"; shift; done\n' "$WORK/calls" > "$WORK/bin/go"
chmod +x "$WORK/bin/"*

cat > "$WORK/sites" <<EOF
# site  ssh
a       user@a-host
EOF
export UPS_MQTT_SITES="$WORK/sites"

# run executes first-install.sh from the repo root; sets OUT, CODE and CALLS.
run() {
  : > "$WORK/calls"
  set +e
  OUT=$(cd "$REPO" && PATH="$WORK/bin:$PATH" bash ./first-install.sh "$@" 2>&1 < /dev/null)
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

# Every ssh and scp must go to the one target.
only_to() {
  local target=$1
  ! grep -A1 -x '== ssh' <<<"$CALLS" | grep -v -x -e '== ssh' -e '--' -e "$target" | grep -q . &&
    ! grep -E '^[^ ]+@[^ ]+:' <<<"$CALLS" | grep -v "^$target:" | grep -q .
}

run --site a
check "site: completes"                 '[[ $CODE == 0 && $OUT == *"remote-setup.sh"* ]]'
check "  …only to the site's host"      'only_to user@a-host && [[ $CALLS == *user@a-host* ]]'
check "  …hint names the site"          '[[ $OUT == *"./deploy.sh --site a"* ]]'
check "  …binary removed"               '[[ -z "$(ls "$REPO"/ups-mqtt-linux-* 2>/dev/null)" ]]'

run --host pi@host
check "host: completes"                 '[[ $CODE == 0 ]] && only_to pi@host'
check "  …hint names the host"          '[[ $OUT == *"./deploy.sh --host pi@host"* ]]'

run
check "no host: exit 2, no calls"       '[[ $CODE == 2 && $OUT == *"name the host"* && $OUT == *"(sites: a)"* && -z $CALLS ]]'
run user@host
check "bare host: exit 2, no calls"     '[[ $CODE == 2 && $OUT == *"--site NAME or --host USER@HOST"* && -z $CALLS ]]'
run --site b
check "unknown site: exit 2, no calls"  '[[ $CODE == 2 && $OUT == *"not in"* && -z $CALLS ]]'
for args in "--host --site" "--site --host" "--host -oProxyCommand=x"; do
  run $args
  check "$args: exit 2, no calls"       '[[ $CODE == 2 && $OUT == *"needs"* && -z $CALLS ]]'
done
run --site a --site a
check "--site twice: exit 2, no calls"  '[[ $CODE == 2 && $OUT == *"given twice"* && -z $CALLS ]]'
: > "$WORK/calls"
set +e
OUT=$(cd / && PATH="$WORK/bin:$PATH" bash "$REPO/first-install.sh" --site a 2>&1 < /dev/null)
CODE=$?
set -e
CALLS=$(cat "$WORK/calls")
check "run from elsewhere: works"       '[[ $CODE == 0 ]] && only_to user@a-host'
run --frobnicate
check "unknown arg: exit 2, no calls"   '[[ $CODE == 2 && $OUT == *"unknown argument"* && -z $CALLS ]]'

echo "first-install: $PASS passed, $FAIL failed"
[[ $FAIL == 0 ]]
