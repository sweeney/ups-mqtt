#!/usr/bin/env bash
#
# recover-ups-usb.sh — recover the CyberPower UPS after its USB link silently
# drops off the bus, WITHOUT rebooting.
#
# It forces a single sleep/wake cycle so macOS tears down and re-enumerates the
# USB subsystem, which brings a dropped/hung device back. A hardware RTC wake is
# scheduled BEFORE sleeping, so a headless/remote machine always comes back on
# its own timer.
#
# This does NOT change the idle-sleep policy ("never sleep" is preserved):
#   - `pmset schedule wake` is a ONE-TIME event, consumed when it fires.
#   - `pmset sleepnow` is a one-shot action, not a setting.
# Nothing here touches the caffeinate/assertion setup that keeps the box awake.
#
# Usage (must run as root):
#   sudo ./scripts/recover-ups-usb.sh          # schedule wake, then sleep now
#   sudo ./scripts/recover-ups-usb.sh verify   # AFTER it wakes: confirm the UPS is back
#
set -euo pipefail

UPS_NAME="cyberpower"
UPS_VENDOR_ID=1892                      # CyberPower USB idVendor 0x0764 == 1892 decimal
DRIVER_LABEL="org.nut.usbhid-ups-cyberpower"
WAKE_MINUTES=3                          # how far ahead to schedule the one-time RTC wake
BACKSTOP_MINUTES=10                     # daily repeating backstop wake, ~this far ahead (insurance)
SLEEP_DELAY_MINUTES=3                   # (schedule mode) minutes from now until the scheduled sleep
DEEP_SLEEP_MINUTES=5                    # (schedule mode) how long to stay asleep before waking
SLEEP_COUNTDOWN=15                      # grace period before forcing sleep (Ctrl-C to abort)
LOG="/tmp/recover-ups-usb.log"
PM_STATE="/tmp/recover-ups-usb.pmstate" # saved tcpkeepalive/powernap values, restored by verify

# ---------------------------------------------------------------------------
log()  { printf '%s  %s\n'        "$(date '+%H:%M:%S')" "$*" | tee -a "$LOG"; }
warn() { printf '%s  WARN: %s\n'  "$(date '+%H:%M:%S')" "$*" | tee -a "$LOG" >&2; }
die()  { printf '%s  ERROR: %s\n' "$(date '+%H:%M:%S')" "$*" | tee -a "$LOG" >&2; exit 1; }

require_root() {
  [[ ${EUID:-$(id -u)} -eq 0 ]] || die "must run as root — use: sudo $0 ${1:-}"
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found in PATH: $1"
}

ups_on_usb() {   # returns 0 if the CyberPower is enumerated on the USB bus
  ioreg -p IOUSB -w0 -l 2>/dev/null | grep -q "\"idVendor\" = ${UPS_VENDOR_ID}"
}

# With tcpkeepalive/powernap enabled, `pmset sleepnow` only reaches DarkWake:
# the USB controller stays powered, nothing re-enumerates, and TCP/SSH survives.
# Disable both so we get a REAL deep sleep that suspends USB. Saved values are
# restored by verify mode (or manually — see the printed command).
save_and_force_deep_sleep() {
  : > "$PM_STATE"
  local k v
  for k in tcpkeepalive powernap; do
    v="$(pmset -g | awk -v key="$k" '$1==key {print $2; exit}')"
    if [[ -n "$v" ]]; then
      printf '%s %s\n' "$k" "$v" >> "$PM_STATE"
      log "Disabling ${k} (was ${v}) to force a real deep sleep."
    else
      log "Disabling ${k} (previous value unknown)."
    fi
    pmset -a "$k" 0 || warn "could not set ${k}=0 — sleep may only reach DarkWake"
  done
}

restore_deep_sleep_settings() {
  if [[ -f "$PM_STATE" ]]; then
    local k v
    while read -r k v; do
      [[ -n "$k" ]] || continue
      log "Restoring ${k}=${v}."
      pmset -a "$k" "$v" || warn "could not restore ${k}=${v} — set it back manually"
    done < "$PM_STATE"
    rm -f "$PM_STATE"
  else
    log "No saved power state found; restoring defaults tcpkeepalive=1, powernap=1."
    pmset -a tcpkeepalive 1 || warn "could not restore tcpkeepalive=1"
    pmset -a powernap 1     || warn "could not restore powernap=1"
  fi
}

# ---------------------------------------------------------------------------
do_sleep() {
  require_root
  require_cmd pmset
  require_cmd date
  require_cmd ioreg
  require_cmd upsc

  log "=== recover-ups-usb: sleep/wake mode ==="

  # Informational: show why we're doing this. Non-fatal if it errors.
  if ups_on_usb; then
    warn "UPS is ALREADY on the USB bus (idVendor ${UPS_VENDOR_ID}). It may not be broken."
    warn "Current status: $(upsc "$UPS_NAME" ups.status 2>&1 | tr '\n' ' ')"
    warn "Continuing anyway; a sleep/wake cycle is harmless."
  else
    log "Confirmed: UPS is NOT on the USB bus. upsc says: $(upsc "$UPS_NAME" ups.status 2>&1 | tr '\n' ' ')"
  fi

  # A live Screen Sharing session holds PreventSystemSleep, which can pin the
  # box in DarkWake even with tcpkeepalive off. Warn loudly if one is present.
  if pmset -g assertions 2>/dev/null | grep -qi 'screensharingd.*PreventSystemSleep'; then
    warn "A Screen Sharing session is holding 'PreventSystemSleep' — this can BLOCK a real sleep."
    warn "Disconnect Screen Sharing/VNC (stay on SSH) before continuing for best results."
  fi

  # Force a genuine deep sleep (not DarkWake) so USB is actually suspended.
  save_and_force_deep_sleep

  # Compute the wake time (now + WAKE_MINUTES). BSD `date -v +NM` == +N minutes.
  local wake_time
  wake_time="$(date -v "+${WAKE_MINUTES}M" '+%m/%d/%Y %H:%M:%S')" \
    || die "failed to compute wake time via 'date -v'"
  [[ -n "$wake_time" ]] || die "computed wake time is empty — aborting"

  log "Scheduling one-time RTC wake at: ${wake_time}"
  pmset schedule wake "$wake_time" \
    || die "'pmset schedule wake' failed — NOT sleeping (would lose a remote box)"

  # Daily repeating backstop wake — insurance in case the one-time wake fails.
  # Fires ~BACKSTOP_MINUTES from now and every day after, until verify cancels it.
  local backstop
  backstop="$(date -v "+${BACKSTOP_MINUTES}M" '+%H:%M:%S')" || backstop=""
  if [[ -n "$backstop" ]]; then
    log "Scheduling daily backstop wake at ${backstop} (verify cancels it)."
    pmset repeat wakeorpoweron MTWRFSU "$backstop" \
      || warn "could not set backstop repeat wake — the one-time wake is still primary"
  else
    warn "could not compute backstop time — continuing with the one-time wake only"
  fi

  # CRITICAL SAFETY CHECK: never sleep unless the wake is actually on the books.
  # Match on the minute (drop seconds) to survive any reformatting by pmset.
  local wake_match="${wake_time%:*}"
  if ! pmset -g sched | grep -qF "$wake_match"; then
    warn "Current schedule:"; pmset -g sched | tee -a "$LOG" >&2
    die "wake at '${wake_match}' not found in 'pmset -g sched' — refusing to sleep"
  fi
  log "Verified wake is scheduled. Current power schedule:"
  pmset -g sched | tee -a "$LOG"

  cat <<EOF | tee -a "$LOG"

------------------------------------------------------------------
The Mac will enter a REAL deep sleep in ${SLEEP_COUNTDOWN}s and WAKE itself
at ${wake_time}. tcpkeepalive/powernap are OFF for this, so your SSH
WILL genuinely drop this time — that dropped connection is the SIGN it
slept for real (last time it only reached DarkWake and SSH survived).

A daily backstop wake is also set as insurance (removed by verify).
Reconnect after it wakes (~${WAKE_MINUTES} min), re-attach tmux, then run:

    sudo $0 verify        # restores tcpkeepalive/powernap + cancels backstop

All processes (including ups-mqtt) just pause and resume — nothing is
killed. tmux survives; SSH does not. "Never sleep" is unchanged; the
wake is one-time only. If you don't run verify, restore settings with:
    sudo pmset -a tcpkeepalive 1 ; sudo pmset -a powernap 1
Press Ctrl-C now to abort.
------------------------------------------------------------------
EOF

  local i
  for ((i=SLEEP_COUNTDOWN; i>0; i--)); do printf '\r  sleeping in %2ds... ' "$i"; sleep 1; done
  printf '\r                       \n'

  log "Forcing sleep now."
  pmset sleepnow || die "'pmset sleepnow' failed"
  # If we get here the request was accepted; the box is on its way down.
  log "Sleep requested. See you after the wake — run: sudo $0 verify"
}

# ---------------------------------------------------------------------------
do_verify() {
  require_root verify
  require_cmd ioreg
  require_cmd upsc
  require_cmd launchctl

  log "=== recover-ups-usb: verify mode ==="

  # Always put the power settings back first, whatever the USB outcome.
  require_cmd pmset
  restore_deep_sleep_settings

  # Remove the daily backstop wake that sleep mode set (no-op if none exist).
  log "Cancelling backstop repeating wake."
  pmset repeat cancel || warn "could not cancel repeating wake — check 'pmset -g sched'"

  # Diagnostic: did the last sleep actually go DEEP, or only DarkWake? This tells
  # us whether the test was even valid (DarkWake = a session/assertion blocked it).
  local last_sleep
  last_sleep="$(pmset -g log 2>/dev/null | grep -iE "Entering (Sleep|DarkWake)" | tail -1)"
  if [[ -n "$last_sleep" ]]; then
    if grep -qi "DarkWake" <<<"$last_sleep"; then
      warn "Last sleep only reached DARKWAKE — deep sleep was blocked, so USB was never suspended:"
      warn "  ${last_sleep}"
      warn "Make sure ALL sessions were disconnected during the scheduled sleep, then retry 'schedule'."
    else
      log "Last sleep reached real deep sleep:"
      log "  ${last_sleep}"
    fi
  fi

  if ups_on_usb; then
    log "UPS IS back on the USB bus (idVendor ${UPS_VENDOR_ID})."
  else
    warn "UPS is STILL NOT on the USB bus."
    warn "The sleep/wake did not re-enumerate it. Remaining remote option is a reboot."
    warn "USB devices currently present:"
    ioreg -p IOUSB -w0 -l 2>/dev/null | grep '"USB Product Name"' | tee -a "$LOG" >&2 || true
    exit 2
  fi

  # Bounce the NUT driver so it re-attaches immediately instead of waiting out
  # its launchd respawn back-off. KeepAlive will restart it.
  log "Kicking the NUT driver (${DRIVER_LABEL})."
  launchctl kickstart -k "system/${DRIVER_LABEL}" \
    || warn "could not kickstart driver; launchd KeepAlive should start it shortly"

  # Poll upsc until the driver reports live data (or we give up).
  local tries=10 status=""
  while (( tries-- > 0 )); do
    status="$(upsc "$UPS_NAME" ups.status 2>&1 || true)"
    case "$status" in
      *stale*|*"not connected"*|*"Driver not"*|"") sleep 3 ;;
      *) break ;;
    esac
  done

  if [[ -z "$status" || "$status" == *stale* || "$status" == *"not connected"* || "$status" == *"Driver not"* ]]; then
    die "UPS is on USB but NUT still not serving data (last: '${status}'). Try again in a few seconds or check /opt/homebrew/var/log/nut-driver.log"
  fi

  log "UPS is live. Readings:"
  upsc "$UPS_NAME" ups.status battery.charge input.voltage ups.load 2>&1 | tee -a "$LOG" || true

  # No need to restart ups-mqtt: it re-dials NUT by itself on its next read,
  # and a restart would only publish online:false to subscribers for nothing
  # (scripts/README.md, "ups-mqtt recovers on its own").
  log "Recovery complete. ups-mqtt reconnects to NUT by itself; confirm MQTT is publishing again."
}

# ---------------------------------------------------------------------------
# Preferred mode for a REMOTE machine: an interactive SSH session holds powerd's
# 'ttyassertion' (NetworkClientActive) which forces DarkWake instead of deep
# sleep. So we schedule the sleep + wake, you DISCONNECT, and it deep-sleeps
# with no session holding it back — then wakes itself on the RTC.
do_schedule() {
  require_root schedule
  require_cmd pmset
  require_cmd date
  require_cmd ioreg
  require_cmd upsc

  log "=== recover-ups-usb: scheduled-sleep mode ==="

  if ups_on_usb; then
    warn "UPS is ALREADY on the USB bus — it may not be broken. Continuing anyway."
  else
    log "Confirmed: UPS is NOT on the USB bus. upsc says: $(upsc "$UPS_NAME" ups.status 2>&1 | tr '\n' ' ')"
  fi

  save_and_force_deep_sleep

  local sleep_at wake_at
  sleep_at="$(date -v "+${SLEEP_DELAY_MINUTES}M" '+%m/%d/%Y %H:%M:%S')" \
    || die "failed to compute sleep time via 'date -v'"
  wake_at="$(date -v "+$((SLEEP_DELAY_MINUTES + DEEP_SLEEP_MINUTES))M" '+%m/%d/%Y %H:%M:%S')" \
    || die "failed to compute wake time via 'date -v'"
  [[ -n "$sleep_at" && -n "$wake_at" ]] || die "computed schedule time was empty — aborting"

  log "Scheduling deep sleep at: ${sleep_at}"
  pmset schedule sleep "$sleep_at" || die "'pmset schedule sleep' failed"
  log "Scheduling wake at:       ${wake_at}"
  pmset schedule wake "$wake_at"  || die "'pmset schedule wake' failed"

  # Daily backstop wake — insurance if the one-time wake ever fails to fire.
  local backstop
  backstop="$(date -v "+$((SLEEP_DELAY_MINUTES + DEEP_SLEEP_MINUTES + 5))M" '+%H:%M:%S')" || backstop=""
  if [[ -n "$backstop" ]]; then
    log "Scheduling daily backstop wake at ${backstop} (verify cancels it)."
    pmset repeat wakeorpoweron MTWRFSU "$backstop" \
      || warn "could not set backstop repeat wake — the one-time wake is still primary"
  fi

  # CRITICAL SAFETY CHECK: confirm the wake is actually on the books.
  local wake_match="${wake_at%:*}"
  if ! pmset -g sched | grep -qF "$wake_match"; then
    warn "Current schedule:"; pmset -g sched | tee -a "$LOG" >&2
    die "wake at '${wake_match}' not scheduled — aborting. Clear with: sudo pmset schedule cancelall"
  fi
  log "Verified. Current power schedule:"
  pmset -g sched | tee -a "$LOG"

  cat <<EOF | tee -a "$LOG"

------------------------------------------------------------------
ACTION REQUIRED — DISCONNECT from this machine NOW.
  * Exit this SSH session completely (not just 'tmux detach' — CLOSE ssh).
  * Ensure no other SSH / Screen Sharing sessions are open to it.

While ANY session is connected, powerd pins the box in DarkWake and USB
never re-enumerates. With you gone, it will:
  * DEEP sleep at ${sleep_at}
  * WAKE itself at ${wake_at}   (+ daily backstop as insurance)

Reconnect AFTER ${wake_at}, re-attach tmux, then run:
    sudo $0 verify

To abort instead: reconnect and run
    sudo pmset schedule cancelall && sudo pmset repeat cancel && \\
    sudo pmset -a tcpkeepalive 1 && sudo pmset -a powernap 1
------------------------------------------------------------------
EOF
  log "Scheduled. Disconnect now — see you after ${wake_at}."
}

case "${1:-schedule}" in
  schedule) do_schedule ;;   # RECOMMENDED for remote: schedule sleep+wake, then disconnect
  sleepnow) do_sleep ;;      # forces immediate sleep — only works from the physical console (no SSH tty)
  verify)   do_verify ;;
  *)        die "unknown mode '${1}'. Usage: sudo $0 [schedule|sleepnow|verify]" ;;
esac
