#!/usr/bin/env bash
# Tests for scripts/live-test.sh and scripts/live-test-watch.sh.  The "host"
# is a fake home directory reached through stub ssh and scp; upsc,
# journalctl, mosquitto_sub and the network tools are stubbed too.  The
# recorders really run, for a second or two.
#
#   bash scripts/test/live-test_test.sh
set -euo pipefail

HERE=$(cd "$(dirname "$0")" && pwd)
SCRIPT="$HERE/../live-test.sh"

PASS=0
FAIL=0
WORK=$(mktemp -d)
FAKEHOME="$WORK/host-home"
OUTDIR="$WORK/runs"
BIN="$WORK/bin"
mkdir -p "$FAKEHOME" "$BIN" "$WORK/state"
echo "nameserver 100.100.100.100" > "$WORK/resolv.conf"
echo "boot-a" > "$WORK/boot_id"

cleanup() {
  # Stop anything a failed test left running.
  for f in "$FAKEHOME"/live-tests/*/pids "$FAKEHOME"/live-tests/*/main.pid "$OUTDIR"/*/sub.pid; do
    [[ -f "$f" ]] || continue
    while read -r p; do kill "$p" 2>/dev/null || true; done < "$f"
  done
  rm -rf "$WORK"
}
trap cleanup EXIT

stub() { printf '#!/usr/bin/env bash\n%s\n' "$2" > "$BIN/$1"; chmod +x "$BIN/$1"; }

# ssh [-n] [-o opt]... HOST CMD... runs CMD locally with the fake home.
# Without -n it reads stdin, as real ssh does, so a controller that forgets
# -n loses the answer to its prompt.
stub ssh 'n=0; while [[ $1 == -o || $1 == -n ]]; do if [[ $1 == -n ]]; then n=1; shift; else shift 2; fi; done; shift
[[ $n == 1 ]] || cat > /dev/null
HOME="'"$FAKEHOME"'" exec bash -c "$*"'
# scp [-q] [-r] SRC DST, with HOST:path meaning the fake home.
stub scp 'args=(); for a in "$@"; do case $a in -q) ;; -r) ;; *:*) args+=("'"$FAKEHOME"'/${a#*:}") ;; *) args+=("$a") ;; esac; done
cp -R "${args[0]}" "${args[1]}"'
stub upsc 'case "$*" in "-l localhost") echo iplug ;; *ups.status) echo OL ;; *battery.voltage) echo 13.4 ;; esac'
stub journalctl 'case " $* " in *" -f "*) exec sleep 1000 ;; esac'
# Keeps its own name in ps, as the real one does (no exec).
stub mosquitto_sub 'echo "2026-10-13T20:00:00+0100 1 ups/site-ups/state {}"; while :; do sleep 1; done'
stub ip 'echo "default via 192.0.2.1 dev eth0"'
stub ping 'exit 0'
stub getent 'exit 0'
stub ss 'echo "ESTAB 0 0 127.0.0.1:5000 127.0.0.1:1883"'

# run executes live-test.sh; sets OUT and CODE.
run() {
  set +e
  OUT=$(PATH="$BIN:$PATH" WATCH_STATE_DIR="$WORK/state" WATCH_RESOLV="$WORK/resolv.conf" WATCH_BOOT_ID="$WORK/boot_id" \
        bash "$SCRIPT" --out "$OUTDIR" "$@" 2>&1 < "${STDIN:-/dev/null}")
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

alive() { local f=$1 p; [[ -s $f ]] || return 1; while read -r p; do kill -0 "$p" 2>/dev/null && return 0; done < "$f"; return 1; }
RUN="$FAKEHOME/live-tests/run1"

# ── arguments ──────────────────────────────────────────────────────────────
run --help
check "help: usage, exit 0"            '[[ $CODE == 0 && $OUT == *"Usage"* ]]'
run --frobnicate host start run1
check "unknown option: exit 2"         '[[ $CODE == 2 && $OUT == *"unknown option"* ]]'
run host start
check "missing name: exit 2"           '[[ $CODE == 2 && $OUT == *"expected HOST COMMAND NAME"* ]]'
run host start "../escape"
check "bad name: exit 2"               '[[ $CODE == 2 && $OUT == *"run name"* ]]'
run host frob run1
check "unknown command: exit 2"        '[[ $CODE == 2 && $OUT == *"unknown command"* ]]'

# ── --check start changes nothing ──────────────────────────────────────────
run --check host start run1
check "check: exit 1"                  '[[ $CODE == 1 ]]'
check "check: says what it would do"   '[[ $OUT == *"Would: record into ~/live-tests/run1"* && $OUT == *"Would: subscribe"* ]]'
check "check: nothing recorded"        '[[ ! -e $RUN ]]'
check "check: no local subscriber"     '[[ ! -e $OUTDIR/run1/sub.pid ]]'
check "check: recorder not copied"    '[[ ! -e $FAKEHOME/live-tests ]]'

# ── start ──────────────────────────────────────────────────────────────────
run host start run1
check "start: exit 0"                  '[[ $CODE == 0 && $OUT == *"recording into"* ]]'
check "start: recorders alive"         'alive "$RUN/pids"'
check "start: subscriber alive"        'alive "$OUTDIR/run1/sub.pid"'
sleep 2
check "start: ups.log recorded"        'grep -q " OL 13.4" "$RUN/ups.log"'
check "start: net.log recorded"        'grep -q "carrier=.* inet=up dns=ok mqtt_conns=1 state=\[\]" "$RUN/net.log"'
check "start: resolv.conf recorded"    'grep -q "100.100.100.100" "$RUN/resolv.log"'
check "start: broker recorded"         'grep -q "ups/site-ups/state" "$OUTDIR/run1/broker.log"'
pids_before=$(cat "$RUN/pids")

# ── start again is a no-op ─────────────────────────────────────────────────
run host start run1
check "restart: exit 0, says so"       '[[ $CODE == 0 && $OUT == *"already recording"* && $OUT == *"already subscribed"* ]]'
check "restart: same recorders"        '[[ "$(cat "$RUN/pids")" == "$pids_before" ]]'
run --check host start run1
check "check while running: exit 0"    '[[ $CODE == 0 && $OUT == *"already recording"* ]]'

run host status run1
check "status: running, subscribed"    '[[ $OUT == *"host:   running"* && $OUT == *"broker: subscribed"* ]]'

# ── stop ───────────────────────────────────────────────────────────────────
run --check host stop run1
check "check stop: exit 1, no change"  '[[ $CODE == 1 ]] && alive "$RUN/pids"'
run host stop run1
check "stop: exit 0"                   '[[ $CODE == 0 ]]'
sleep 0.3
check "stop: recorders gone"           '! alive "$RUN/pids"'
check "stop: subscriber gone"          '[[ ! -e $OUTDIR/run1/sub.pid ]]'
check "stop: stopped-at written"       '[[ -s $RUN/stopped-at ]]'
run host status run1
check "status: stopped"                '[[ $OUT == *"host:   stopped"* && $OUT == *"broker: not subscribed"* ]]'
run host stop run1
check "stop again: exit 0"             '[[ $CODE == 0 && $OUT == *"not recording"* ]]'

# ── collect ────────────────────────────────────────────────────────────────
run --check host collect run1
check "check collect: exit 1"          '[[ $CODE == 1 && ! -e $OUTDIR/run1/host ]]'
run host collect run1
check "collect: exit 0"                '[[ $CODE == 0 && -s $OUTDIR/run1/host/ups.log ]]'
run host collect run1
check "collect again: replaces"        '[[ $CODE == 0 && -s $OUTDIR/run1/host/ups.log && ! -e $OUTDIR/run1/host.tmp ]]'
run host collect nosuch
check "collect missing run: exit 2"    '[[ $CODE == 2 && $OUT == *"no run nosuch"* ]]'

# ── starting over an earlier run asks first ────────────────────────────────
run host start run1
check "rerun, no answer: aborts"       '[[ $CODE == 1 && $OUT == *"Aborted"* && -s $RUN/stopped-at ]]'
echo y > "$WORK/yes"
STDIN="$WORK/yes" run host start run1
check "rerun, yes: records afresh"     '[[ $CODE == 0 && ! -e $RUN/stopped-at ]] && alive "$RUN/pids"'
run host stop run1
run --yes host start run1
check "rerun, --yes: records afresh"   '[[ $CODE == 0 && ! -e $RUN/stopped-at ]] && alive "$RUN/pids"'
run host stop run1

# ── scrub ──────────────────────────────────────────────────────────────────
SRC="$WORK/raw"; DEST="$WORK/clean"
mkdir -p "$SRC/sub"
echo "Oct 13 12:00:00 secret-host ups-mqtt: connected to tcp://10.1.2.3:1883" > "$SRC/journal.log"
echo "carrier=1 inet=up 1.1.1.1" > "$SRC/sub/net.log"
echo 4242 > "$SRC/pids"
printf 's/secret-host/ups-host/g\ns/10\\.1\\.2\\.3/192.0.2.3/g\n' > "$WORK/map.sed"
printf 's/secret-host/ups-host/g\n' > "$WORK/partial.sed"

run --check scrub "$WORK/map.sed" "$SRC" "$DEST"
check "check scrub: exit 1, no dest"   '[[ $CODE == 1 && ! -e $DEST && $OUT == *"2 patterns"* ]]'
run scrub "$WORK/map.sed" "$SRC" "$DEST"
check "scrub: exit 0"                  '[[ $CODE == 0 ]]'
check "scrub: replaced"                'grep -q "ups-host ups-mqtt: connected to tcp://192.0.2.3:1883" "$DEST/journal.log"'
check "scrub: keeps tree"              '[[ -f $DEST/sub/net.log ]]'
check "scrub: drops pid files"         '[[ ! -e $DEST/pids ]]'
check "scrub: allowed IP kept"         'grep -q "1.1.1.1" "$DEST/sub/net.log"'

run scrub "$WORK/partial.sed" "$SRC" "$WORK/clean2"
check "leftover IP: exit 2"            '[[ $CODE == 2 && $OUT == *"addresses left: 10.1.2.3"* ]]'
check "leftover IP: nothing written"   '[[ ! -e $WORK/clean2 ]]'

printf 's/secret/redacted/g\n' > "$WORK/weak.sed"
printf 's/secret-host/x/g\ns/10\\.1\\.2\\.3/192.0.2.3/g\ns/never-there/y/g\n' > "$WORK/ok.sed"
run scrub "$WORK/ok.sed" "$SRC" "$WORK/clean3"
check "unmatched pattern is fine"      '[[ $CODE == 0 ]]'

run scrub "$WORK/map.sed" "$SRC" "$DEST"
check "dest exists, no answer: aborts" '[[ $CODE == 1 && $OUT == *"Aborted"* ]]'
run --yes scrub "$WORK/map.sed" "$SRC" "$DEST"
check "dest exists, --yes: replaces"   '[[ $CODE == 0 ]]'

REPO_MAP="$HERE/../../.live-test-map.sed"
trap 'rm -f "$REPO_MAP"; cleanup' EXIT
cp "$WORK/map.sed" "$REPO_MAP"
run scrub "$REPO_MAP" "$SRC" "$WORK/clean4"
rm -f "$REPO_MAP"
check "map inside repo: refused"       '[[ $CODE == 2 && $OUT == *"outside the repo"* && ! -e $WORK/clean4 ]]'

run scrub "$WORK/nope.sed" "$SRC" "$DEST"
check "missing map: exit 2"            '[[ $CODE == 2 && $OUT == *"map not found"* ]]'

# ── scrub: what slips past a naive check ───────────────────────────────────
SRC2="$WORK/raw2"
mkdir -p "$SRC2"
printf 'dhcp from 10.9.8.7\0binary\n' > "$SRC2/binary.log"
run scrub "$WORK/map.sed" "$SRC2" "$WORK/c5"
check "binary file IP: caught"         '[[ $CODE == 2 && $OUT == *"10.9.8.7"* && ! -e $WORK/c5 ]]'

rm -rf "$SRC2"; mkdir -p "$SRC2"
echo "mdns: SECRET-HOST.local" > "$SRC2/a.log"
run scrub "$WORK/map.sed" "$SRC2" "$WORK/c6"
check "other case: caught"             '[[ $CODE == 2 && $OUT == *"ignoring case"* && ! -e $WORK/c6 ]]'

rm -rf "$SRC2"; mkdir -p "$SRC2"
echo "link fe80::ba27:ebff:fe69:2e10 hw b8:27:eb:69:2e:10 ts fd7a:115c:a1e0::53" > "$SRC2/a.log"
run scrub "$WORK/map.sed" "$SRC2" "$WORK/c7"
check "IPv6 and MAC: caught"           '[[ $CODE == 2 && $OUT == *"b8:27:eb:69:2e:10"* && $OUT == *"ba27:ebff:fe69:2e10"* ]]'
check "  Tailscale resolver allowed"   '[[ $OUT != *"fd7a:115c:a1e0::53"* ]]'

rm -rf "$SRC2"; mkdir -p "$SRC2"
echo "fine" > "$SRC2/secret-host.log"
run scrub "$WORK/map.sed" "$SRC2" "$WORK/c8"
check "file name scrubbed"             '[[ $CODE == 0 && -f $WORK/c8/ups-host.log && ! -e $WORK/c8/secret-host.log ]]'

printf 's/a\\/b/x/g\n' > "$WORK/escaped.sed"
run scrub "$WORK/escaped.sed" "$SRC" "$WORK/c9"
check "escaped delimiter: refused"     '[[ $CODE == 2 && $OUT == *"escaped delimiter"* ]]'
printf 's/a/b/;s/c/d/\n' > "$WORK/two.sed"
run scrub "$WORK/two.sed" "$SRC" "$WORK/c9"
check "two commands a line: refused"   '[[ $CODE == 2 && $OUT == *"one s command per line"* ]]'
printf '# a comment\n\ns|secret-host|ups-host|g\ns|10\\.1\\.2\\.3|192.0.2.3|\n' > "$WORK/pipes.sed"
run scrub "$WORK/pipes.sed" "$SRC" "$WORK/c10"
check "comments and | delimiter: ok"   '[[ $CODE == 0 ]]'

# ── host-side guards ───────────────────────────────────────────────────────
run host start run2
check "run2: started"                  '[[ $CODE == 0 ]] && alive "$FAKEHOME/live-tests/run2/pids"'
echo "boot-b" > "$WORK/boot_id"   # the host rebooted
run host status run2
check "after reboot: stopped"          '[[ $OUT == *"host:   stopped"* ]]'
run host stop run2
check "after reboot: stop is a no-op"  '[[ $CODE == 0 && $OUT == *"not recording"* ]]'
check "  …and signals nothing"         'alive "$FAKEHOME/live-tests/run2/pids"'
echo "boot-a" > "$WORK/boot_id"
run host stop run2

run --check host stop run2
check "check stop, nothing running"    '[[ $CODE == 0 && $OUT == *"Nothing to stop"* ]]'

# A prompt answered on stdin still gets its answer: ssh must not eat it.
echo y > "$WORK/yes"
STDIN="$WORK/yes" run host start run2
check "piped yes reaches the prompt"   '[[ $CODE == 0 ]] && alive "$FAKEHOME/live-tests/run2/pids"'
run host stop run2

# The subscriber failing at once is an error, not "subscribed".
stub mosquitto_sub 'echo "Error: Connection refused" >&2; exit 1'
run host start run3
check "subscriber fails: exit 2"       '[[ $CODE == 2 && $OUT == *"Connection refused"* && $OUT == *"stop it with"* ]]'
run host stop run3

# A host whose upsc lists no UPS is refused before anything starts.
stub upsc 'exit 0'
run host start run4
check "no UPS: refused"                '[[ $CODE == 2 && $OUT == *"lists no UPS"* && ! -e $FAKEHOME/live-tests/run4/pids ]]'

echo "live-test: $PASS passed, $FAIL failed"
[[ $FAIL == 0 ]]
