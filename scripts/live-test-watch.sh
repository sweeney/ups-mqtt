#!/usr/bin/env bash
# live-test-watch.sh — the host side of scripts/live-test.sh.  Records, once a
# second, what the UPS, the network and ups-mqtt are doing during a live
# power-cut test.  live-test.sh copies it to the host and runs it there; it is
# not normally run by hand.
#
# Usage (on the host):
#   live-test-watch.sh start DIR BROKER_HOST   # start recording into DIR
#   live-test-watch.sh stop DIR                # stop recording
#   live-test-watch.sh status DIR              # running | stopped | absent
#
# start is idempotent: if DIR is already being recorded it says so and
# leaves it alone.  It refuses a DIR that holds an earlier, stopped run;
# live-test.sh asks before clearing one.
#
# Files written to DIR:
#   ups.log      HH:MM:SS.mmm ups.status battery.voltage (upsc, every 1 s)
#   net.log      link carrier, internet, broker name, MQTT connections,
#                ups-mqtt state dir (every 1 s)
#   resolv.log   /etc/resolv.conf, each time it changes
#   journal.log  ups-mqtt, mosquitto, NetworkManager, tailscaled and NUT
#   kernel.log   kernel messages (link up/down, undervoltage)
#   started-at, stopped-at   epoch seconds;  pids   the recorders
#
# WATCH_STATE_DIR (default /var/lib/ups-mqtt) and WATCH_RESOLV (default
# /etc/resolv.conf) exist so tests can point it at fixtures.
set -euo pipefail

STATE_DIR="${WATCH_STATE_DIR:-/var/lib/ups-mqtt}"
RESOLV="${WATCH_RESOLV:-/etc/resolv.conf}"

die() { echo "ERROR: $*" >&2; exit 2; }

running() {
  local dir=$1 pid
  [[ -s "$dir/pids" ]] || return 1
  while read -r pid; do
    kill -0 "$pid" 2>/dev/null && return 0
  done < "$dir/pids"
  return 1
}

# The interface carrying the default route, for its carrier state.
default_iface() {
  ip route show default 2>/dev/null | awk '{for (i = 1; i < NF; i++) if ($i == "dev") { print $(i + 1); exit }}'
}

record() {
  local dir=$1 broker=$2 ups iface
  ups=$(upsc -l localhost 2>/dev/null | head -1 || true)
  iface=$(default_iface || true)
  : > "$dir/pids"

  ( while :; do
      echo "$(date +%T.%3N) $(upsc "$ups@localhost" ups.status 2>/dev/null) $(upsc "$ups@localhost" battery.voltage 2>/dev/null)"
      sleep 1
    done ) >> "$dir/ups.log" 2>&1 &
  echo $! >> "$dir/pids"

  ( while :; do
      carrier=$(cat "/sys/class/net/${iface:-eth0}/carrier" 2>/dev/null || echo "?")
      inet=$(ping -c1 -W1 1.1.1.1 >/dev/null 2>&1 && echo up || echo DOWN)
      dns=$(getent hosts "$broker" >/dev/null 2>&1 && echo ok || echo FAIL)
      conns=$(ss -tn state established 2>/dev/null | grep -c ':1883' || true)
      state=$(ls "$STATE_DIR" 2>&1 | tr '\n' ' ')
      echo "$(date +%T) carrier=$carrier inet=$inet dns=$dns mqtt_conns=$conns state=[$state]"
      sleep 1
    done ) >> "$dir/net.log" 2>&1 &
  echo $! >> "$dir/pids"

  ( last=""
    while :; do
      now=$(cat "$RESOLV" 2>/dev/null || true)
      if [[ "$now" != "$last" ]]; then
        { echo "== $(date +%T)"; echo "$now"; } >> "$dir/resolv.log"
        last=$now
      fi
      sleep 1
    done ) &
  echo $! >> "$dir/pids"

  # Separate journalctl calls: -k and -u together match neither.
  journalctl -f -n0 -o short-precise -u ups-mqtt -u mosquitto -u NetworkManager \
    -u tailscaled -u nut-server -u nut-monitor -u 'nut-driver@*' >> "$dir/journal.log" 2>&1 &
  echo $! >> "$dir/pids"
  journalctl -f -n0 -k -o short-precise >> "$dir/kernel.log" 2>&1 &
  echo $! >> "$dir/pids"

  wait
}

cmd=${1:-}
case "$cmd" in
  start)
    [[ $# -eq 3 ]] || die "usage: $0 start DIR BROKER_HOST"
    dir=$2 broker=$3
    if running "$dir"; then
      echo "already recording into $dir"
      exit 0
    fi
    [[ ! -e "$dir/started-at" ]] || die "$dir holds an earlier run; clear it first"
    for tool in upsc journalctl; do
      command -v "$tool" >/dev/null || die "$tool not found on this host"
    done
    mkdir -p "$dir"
    date +%s > "$dir/started-at"
    # nohup and closed stdio so the recorders outlive the ssh session.
    nohup bash "$0" _record "$dir" "$broker" > "$dir/watch.err" 2>&1 < /dev/null &
    echo $! > "$dir/main.pid"
    for _ in 1 2 3 4 5 6 7 8 9 10; do
      [[ -s "$dir/pids" ]] && break
      sleep 0.2
    done
    running "$dir" || die "recorders did not start; see $dir/watch.err"
    echo "recording into $dir"
    ;;
  _record)
    record "$2" "$3"
    ;;
  stop)
    [[ $# -eq 2 ]] || die "usage: $0 stop DIR"
    dir=$2
    if ! running "$dir"; then
      echo "not recording into $dir"
      exit 0
    fi
    while read -r pid; do kill "$pid" 2>/dev/null || true; done < "$dir/pids"
    [[ -s "$dir/main.pid" ]] && kill "$(cat "$dir/main.pid")" 2>/dev/null || true
    date +%s > "$dir/stopped-at"
    echo "stopped recording into $dir"
    ;;
  status)
    [[ $# -eq 2 ]] || die "usage: $0 status DIR"
    dir=$2
    if running "$2"; then echo running
    elif [[ -e "$dir/started-at" ]]; then echo stopped
    else echo absent
    fi
    ;;
  *)
    die "usage: $0 start|stop|status DIR ..."
    ;;
esac
