#!/usr/bin/env bash
# live-test.sh — record a live power-cut test, then sanitise it for the repo.
#
# A live test (docs/live-tests.md) cuts mains to a UPS and records what the
# UPS, the host's network, ups-mqtt and the broker do.  This runs the
# recorders: scripts/live-test-watch.sh on the UPS host, over ssh, and a
# subscriber on the broker from here.
#
# Usage:
#   live-test.sh [opts] HOST start NAME     # start recording run NAME
#   live-test.sh [opts] HOST status NAME
#   live-test.sh [opts] HOST stop NAME      # stop recording
#   live-test.sh [opts] HOST collect NAME   # copy the host's files here
#   live-test.sh [opts] scrub MAP SRC DEST  # sanitise a run into the repo
#
# Options:
#   --broker HOST   broker to subscribe to (default: the host itself)
#   --topic FILTER  topic filter to record; repeatable (default ups/# sites/#)
#   --out DIR       where runs are kept locally (default ./live-test-runs)
#   --check         say what would be done, change nothing
#   --yes, -y       do not ask before replacing an earlier run or DEST
#
# A run lives in ~/live-tests/NAME on the host and OUT/NAME here.  start and
# stop are idempotent.  start asks before clearing an earlier run of the same
# NAME.
#
# scrub copies SRC to DEST, applying MAP — a sed -E script of s/…/…/g lines
# kept OUTSIDE the repo, since it names the hosts, sites and addresses it
# removes — then fails if any left-hand side still matches, or if an IPv4
# address outside the documentation ranges remains.  Review DEST before
# committing it.
#
# Exit codes: 0 ok, 1 aborted at a prompt or (--check) work to do, 2 error.
set -euo pipefail

HERE=$(cd "$(dirname "$0")" && pwd)
WATCH="$HERE/live-test-watch.sh"
REMOTE_BASE="live-tests"   # under the remote user's home

die() { echo "ERROR: $*" >&2; exit 2; }
say() { echo "==> $*"; }
usage() { sed -n '2,/^set -euo/p' "$0" | grep '^#' | sed 's/^# \{0,1\}//'; }

CHECK=0 YES=0 BROKER="" OUT="./live-test-runs"
TOPICS=()
ARGS=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --check|--dry-run) CHECK=1 ;;
    --yes|-y)          YES=1 ;;
    --broker)          [[ $# -ge 2 ]] || die "--broker needs a host"; BROKER=$2; shift ;;
    --topic)           [[ $# -ge 2 ]] || die "--topic needs a filter"; TOPICS+=("$2"); shift ;;
    --out)             [[ $# -ge 2 ]] || die "--out needs a directory"; OUT=$2; shift ;;
    -h|--help)         usage; exit 0 ;;
    -*)                usage >&2; die "unknown option: $1" ;;
    *)                 ARGS+=("$1") ;;
  esac
  shift
done
[[ ${#TOPICS[@]} -gt 0 ]] || TOPICS=("ups/#" "sites/#")

confirm() {
  [[ $YES == 1 ]] && return 0
  local answer
  read -r -p "$1 [y/N] " answer || answer=""
  case "$answer" in y|Y|yes|YES) return 0 ;; esac
  echo "Aborted; nothing changed."
  exit 1
}

valid_name() {
  [[ "$1" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]] || die "run name must be letters, digits, . _ -: $1"
}

# ── scrub ──────────────────────────────────────────────────────────────────
TMP=""
trap '[[ -z "$TMP" ]] || rm -rf "$TMP"' EXIT

# Addresses that identify nothing: documentation ranges, Tailscale's shared
# resolver, the CGNAT block the archive renumbers into, loopback.
SAFE_IPV4='^(192\.0\.2\.|198\.51\.100\.|203\.0\.113\.|100\.64\.0\.|127\.|0\.0\.0\.0$|1\.1\.1\.1$|100\.100\.100\.100$)'
SAFE_IPV6='^(fd7a:115c:a1e0::53$|2001:db8:)'

# map_patterns prints the left-hand side of each s/LHS/RHS/flags line, so
# the copy can be checked for anything still matching.  Lines it cannot
# check reliably are refused rather than guessed at.
map_patterns() {
  local map=$1 line d rest lhs after rhs n=0
  while IFS= read -r line || [[ -n "$line" ]]; do
    n=$((n + 1))
    [[ -z "${line//[[:space:]]/}" || $line == \#* ]] && continue
    [[ ${#line} -ge 4 && ${line:0:1} == s ]] || die "$map:$n: not an s command: $line"
    d=${line:1:1} rest=${line:2}
    lhs=${rest%%"$d"*}
    [[ $lhs != *\\ ]] || die "$map:$n: escaped delimiter in the pattern; use another delimiter (s|…|…|g)"
    after=${rest#*"$d"}; rhs=${after%%"$d"*}; after=${after#*"$d"}
    [[ $after =~ ^[gIi0-9]*$ ]] || die "$map:$n: one s command per line, flags only after it"
    [[ -n $lhs ]] || die "$map:$n: empty pattern"
    : "$rhs"
    printf '%s\n' "$lhs"
  done < "$map"
}

scrub() {
  local map=$1 src=$2 dest=$3
  [[ -f "$map" ]] || die "map not found: $map"
  [[ -d "$src" ]] || die "source run not found: $src"
  case "$(cd "$(dirname "$map")" && pwd)/" in
    "$(cd "$HERE/.." && pwd)"/*) die "the map must live outside the repo: it names what it removes" ;;
  esac
  local patterns
  patterns=$(map_patterns "$map") || exit 2
  [[ -n "$patterns" ]] || die "$map has no s/…/…/ lines"

  if [[ $CHECK == 1 ]]; then
    echo "Would: copy $src to $dest through $map ($(wc -l <<<"$patterns" | tr -d ' ') patterns) and check for leftovers"
    [[ -e "$dest" ]] && echo "Would: replace the existing $dest (asks first)"
    exit 1
  fi
  if [[ -e "$dest" ]]; then
    confirm "$dest exists. Replace it?"
  fi

  # Bytes, not characters: journals can hold invalid UTF-8, which would make
  # sed fail and grep treat a file as binary and skip it.
  export LC_ALL=C
  TMP=$(mktemp -d)
  local f out
  while IFS= read -r f; do
    out=$(sed -E -f "$map" <<<"$f")   # file names can carry identifiers too
    mkdir -p "$TMP/$(dirname "$out")"
    sed -E -f "$map" "$src/$f" > "$TMP/$out" || die "sed failed on $src/$f"
  done < <(cd "$src" && find . -type f ! -name pids ! -name main.pid ! -name watch.err ! -name sub.pid)

  local leftover="" p rc
  while IFS= read -r p; do
    rc=0
    grep -rqaiE -- "$p" "$TMP" || rc=$?
    [[ $rc -le 1 ]] || die "cannot check pattern $p (grep exit $rc)"
    [[ $rc -eq 1 ]] || leftover+="  pattern still matches (ignoring case): $p"$'\n'
    if (cd "$TMP" && find . | grep -qaiE -- "$p"); then leftover+="  pattern still in a file name: $p"$'\n'; fi
  done <<<"$patterns"

  local ips
  ips=$(grep -rhoaE '([0-9]{1,3}\.){3}[0-9]{1,3}' "$TMP" | sort -u | grep -vE "$SAFE_IPV4" || true)
  [[ -z "$ips" ]] || leftover+="  IPv4 addresses left: $(tr '\n' ' ' <<<"$ips")"$'\n'
  ips=$(grep -rhoaiE '[0-9a-f]{1,4}(:[0-9a-f]{0,4}){3,7}' "$TMP" | tr 'A-F' 'a-f' | sort -u | grep -vE "$SAFE_IPV6" || true)
  [[ -z "$ips" ]] || leftover+="  IPv6 or MAC addresses left: $(tr '\n' ' ' <<<"$ips")"$'\n'
  if [[ -n "$leftover" ]]; then
    printf '%s' "$leftover" >&2
    die "not sanitised; extend $map and run again (nothing written)"
  fi

  rm -rf "$dest"
  mkdir -p "$(dirname "$dest")"
  cp -R "$TMP" "$dest"
  say "sanitised $src into $dest — review it before committing"
}

if [[ ${ARGS[0]:-} == scrub ]]; then
  [[ ${#ARGS[@]} -eq 4 ]] || die "usage: $0 scrub MAP SRC DEST"
  scrub "${ARGS[1]}" "${ARGS[2]}" "${ARGS[3]}"
  exit 0
fi

# ── host commands ──────────────────────────────────────────────────────────
[[ ${#ARGS[@]} -eq 3 ]] || { usage >&2; die "expected HOST COMMAND NAME"; }
HOST=${ARGS[0]} CMD=${ARGS[1]} NAME=${ARGS[2]}
valid_name "$NAME"
[[ -n "$BROKER" ]] || BROKER=${HOST#*@}
REMOTE_DIR="$REMOTE_BASE/$NAME"
LOCAL_DIR="$OUT/$NAME"

# -n: never read this script's stdin, which a prompt may need.
rsh() { ssh -n -o BatchMode=yes -o ConnectTimeout=10 "$HOST" "$@"; }

remote_status() {
  rsh "bash ~/$REMOTE_BASE/live-test-watch.sh status ~/$REMOTE_DIR" 2>/dev/null || echo unknown
}

# local_running: the subscriber's PID is alive and is still mosquitto_sub.
local_running() {
  [[ -s "$LOCAL_DIR/sub.pid" ]] || return 1
  ps -p "$(cat "$LOCAL_DIR/sub.pid")" -o command= 2>/dev/null | grep -q mosquitto_sub
}

stop_local() {
  if local_running; then
    kill "$(cat "$LOCAL_DIR/sub.pid")" 2>/dev/null || true
    say "stopped the local subscriber"
  fi
  rm -f "$LOCAL_DIR/sub.pid"
}

case "$CMD" in
  start)
    rsh true || die "cannot ssh to $HOST"
    command -v mosquitto_sub >/dev/null || die "mosquitto_sub not found here"
    rsh "command -v upsc >/dev/null && command -v journalctl >/dev/null" \
      || die "$HOST lacks upsc or journalctl"
    if [[ $CHECK == 1 ]]; then
      # The recorder may not be on the host yet; without it, nothing is.
      status=$(rsh "[ -f ~/$REMOTE_BASE/live-test-watch.sh ] && bash ~/$REMOTE_BASE/live-test-watch.sh status ~/$REMOTE_DIR || echo absent" 2>/dev/null || echo unknown)
      case "$status" in
        running) echo "Host is already recording run $NAME." ;;
        stopped) echo "Would: clear the earlier run $NAME on $HOST (asks first), then record into ~/$REMOTE_DIR" ;;
        *)       echo "Would: record into ~/$REMOTE_DIR on $HOST" ;;
      esac
      local_running && echo "Already subscribed to $BROKER." \
        || echo "Would: subscribe to ${TOPICS[*]} on $BROKER into $LOCAL_DIR/broker.log"
      [[ $status == running ]] && local_running && exit 0
      exit 1
    fi
    rsh "mkdir -p ~/$REMOTE_BASE" && scp -q "$WATCH" "$HOST:$REMOTE_BASE/live-test-watch.sh" \
      || die "cannot copy the recorder to $HOST"
    status=$(remote_status)
    if [[ $status == stopped ]]; then
      confirm "Run $NAME already exists on $HOST, and here in $LOCAL_DIR (anything collected is deleted too). Clear both and record again?"
      stop_local
      rsh "rm -rf ~/$REMOTE_DIR"
      rm -rf "$LOCAL_DIR"
    fi
    rsh "bash ~/$REMOTE_BASE/live-test-watch.sh start ~/$REMOTE_DIR $(printf %q "$BROKER")"
    mkdir -p "$LOCAL_DIR"
    if local_running; then
      say "already subscribed to $BROKER"
    else
      args=()
      for t in "${TOPICS[@]}"; do args+=(-t "$t"); done
      nohup mosquitto_sub -h "$BROKER" "${args[@]}" -v -F '%I %r %t %p' >> "$LOCAL_DIR/broker.log" 2> "$LOCAL_DIR/sub.err" < /dev/null &
      echo $! > "$LOCAL_DIR/sub.pid"
      sleep 1
      if ! local_running; then
        cat "$LOCAL_DIR/sub.err" >&2 || true
        die "mosquitto_sub exited at once; the host is recording — stop it with: $0 $HOST stop $NAME"
      fi
      say "subscribed to ${TOPICS[*]} on $BROKER → $LOCAL_DIR/broker.log"
    fi
    say "recording run $NAME; stop with: $0 $HOST stop $NAME"
    ;;
  status)
    echo "host:   $(remote_status)"
    if local_running; then echo "broker: subscribed"; else echo "broker: not subscribed"; fi
    ;;
  stop)
    if [[ $CHECK == 1 ]]; then
      status=$(remote_status)
      if [[ $status != running ]] && ! local_running; then
        echo "Nothing to stop (host: $status)."
        exit 0
      fi
      echo "Would: stop recording run $NAME on $HOST (host: $status) and the local subscriber"
      exit 1
    fi
    remote_ok=1
    rsh "bash ~/$REMOTE_BASE/live-test-watch.sh stop ~/$REMOTE_DIR" || remote_ok=0
    stop_local
    [[ $remote_ok == 1 ]] || die "could not reach $HOST to stop it; it may still be recording"
    ;;
  collect)
    [[ $(remote_status) != absent ]] || die "no run $NAME on $HOST"
    if [[ $CHECK == 1 ]]; then
      echo "Would: copy ~/$REMOTE_DIR from $HOST into $LOCAL_DIR/host"
      exit 1
    fi
    mkdir -p "$LOCAL_DIR"
    rm -rf "$LOCAL_DIR/host.tmp"
    scp -q -r "$HOST:$REMOTE_DIR" "$LOCAL_DIR/host.tmp" || die "copy from $HOST failed"
    rm -rf "$LOCAL_DIR/host"
    mv "$LOCAL_DIR/host.tmp" "$LOCAL_DIR/host"
    say "collected into $LOCAL_DIR/host"
    ;;
  *)
    usage >&2
    die "unknown command: $CMD"
    ;;
esac
