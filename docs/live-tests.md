# Live tests

Tests run against real hardware in production: cutting mains to a UPS and
watching what ups-mqtt, the host and the broker actually do. Each entry
records what happened and the failure modes it exposed; the raw captures
are under `testdata/live-tests/<date>/` as the source for test fixtures.

## Running one

`scripts/live-test.sh` records a run. From your machine, before the cut:

```bash
scripts/live-test.sh --broker <main-broker> <user@ups-host> start <name>
```

This records, every second on the host:
- the UPS status and battery voltage;
- the link, internet and broker-name resolution, plus the TCP connections on port 1883 and the ups-mqtt state directory;
- every change to `resolv.conf`;
- the journal and kernel log;
- every UPS variable every 2 s (`listvar.log`), in upsd's `LIST VAR` wire format, so a snapshot can be copied into `internal/nut/testdata/` as a fixture.

It also subscribes to `ups/#` and `sites/#` on the broker. Behind the local bridge, point `--broker` at the main broker: that is where subscribers see the result. Both sides of the bridge are recorded from your machine. The host's own broker listens only on localhost, so it is read over ssh into `host-broker.log`, each line prefixed with when it arrived, reconnecting if the path drops. A ping a second goes into `link.log`, so a gap can be put down to the uplink or to the bridge.

When power has been back long enough:

```bash
scripts/live-test.sh <user@ups-host> stop <name>
scripts/live-test.sh <user@ups-host> collect <name>
```

The run is now under `live-test-runs/<name>/` here, which is ignored by git. To add it as a fixture, sanitise it with a sed map that you keep *outside* the repo, since the map names what it removes:

```bash
scripts/live-test.sh scrub ~/ups-live-test.sed live-test-runs/<name> testdata/live-tests/<date>/<n>-<what>
```

`scrub` refuses to write if a pattern from the map, or an address outside the documentation ranges, is still there. Then review the result, and add a section below for the run.

## 2026-10-13: mains cut, UPS feeding the Pi (Riello iPlug)

Data: [`testdata/live-tests/2026-10-13/`](../testdata/live-tests/2026-10-13/).

Production config for tests 1–3: poll 10 s, broker `tcp://broker:1883` by
Tailscale MagicDNS short name, paho auto-reconnect with default backoff,
5 s publish timeout, events at QoS 1. Tests 4 and 5 ran later that evening,
after the fixes they led to.

### 1. Network gear unpowered (`1-network-unpowered/`)

The switch/router were on a surge-only outlet, so cutting mains took the
Pi's network down with it while the Pi itself stayed up on the UPS.

| Time | Event |
|---|---|
| 12:57:33 | eth0 link down (kernel) |
| 12:57:35 | ups-mqtt: outage detected; `outage.json` written |
| 12:57:36 | `upsc`: OB, 13.4 → 13.2 V |
| 12:57:53 | eth0 link up |
| 12:57:54 | `upsc`: OL (≈20 s on battery) |
| 12:58:05 | ups-mqtt: power restored (polls stretched to ≈30 s, see below) |
| 12:58:11 | broker publishes the LWT `online:false` (keep-alive expired) |
| 12:58:10–12:58:51 | two more link flaps while the router boots |
| 12:58:57 | DHCP lease; NetworkManager overwrites `resolv.conf`; tailscaled detects the "trample" and restores its own within 70 ms |
| 12:59:45 | link to broker back; paho flushes its queue: outage, five clears, retained `last_outage` (30 s, should be ≈20 s) |

The 1 s/2 s `upsc` samples from this run were lost; the OB/OL times above
were read from them before deletion. `broker-state.log` has the `variables`
object elided.

### 2. DNS broken, then a cut (`2-dns-broken/`)

**13:19–13:20.** While the network gear was being moved to battery outlets,
eth0 flapped three times. On the final DHCP lease (13:20:21)
NetworkManager rewrote `/etc/resolv.conf` to `nameserver 192.0.2.1` /
`search localdomain`, and this time tailscaled logged no trample and did not
restore it. `broker` stopped resolving. Tailscale itself stayed up; the
broker was reachable by IP throughout.

**13:20–19:57.** ups-mqtt never noticed beyond `publishing … timed out after
5s`: one per message, ≈5 per poll, so each poll took ≈30 s
(`timeouts-per-minute.txt`). Nothing was logged about the lost connection or
failed reconnects. The broker kept the LWT `online:false` the whole time.

**19:46:50–19:50:35, the cut.** Network stayed up (carrier and internet up
in every `net.log` sample). The Pi logged `Undervoltage detected` at
19:49:41 for 4 s while on battery (`kernel.log`). ups-mqtt detected the
outage at 19:47:03 (13 s late) and restored at 19:51:03 (≈28 s late), both
from the 30 s polls. Nothing reached the broker.

**19:52:47.** `tailscaled` restarted by hand; it rewrote `resolv.conf` and
`broker` resolved again. paho did not reconnect within 4 minutes (its
reconnect backoff had grown towards the 10 min default cap).

**19:56:51.** `systemctl restart ups-mqtt`. The old process took 38 s to
exit (offline announcement and drain). On its way out it reconnected and
the broker received **≈4,650 queued messages in 2 s** (19:57:28–29):
6½ hours of `state` and `computed/*` (payload timestamps 11:48Z–18:57Z)
plus the cut's `outage` payloads and clears, **out of order**: 408 `state`
messages arrived older than the one before, `outage` payloads for
18:50:03Z arrived before 18:47:03Z, interleaved with clears. The last
`outage` delivered happened to be a clear; had it been a payload, the
broker would have retained "on battery" after mains returned.

**19:57:29.** New process connected at once, found the unreported outage in
`outage.json`, cleared `outage` and published retained `last_outage`
18:47:03Z → 18:51:03Z, 240 s, `spans_restart: false` (actual ≈225 s).

### 3. Clean run (`3-clean/`)

Network gear on battery outlets, DNS healthy, ups-mqtt connected. The
baseline: everything working as designed.

| Time | Event |
|---|---|
| 20:04:42.6 | `upsc`: OB, 13.4 → 13.1 V |
| 20:04:49 | ups-mqtt: outage detected; `outage` on the broker the same second |
| 20:04:49–20:10:29 | `outage` every 10 s, `outage_duration_secs` 0 → 340 |
| 20:10:30.1 | `upsc`: OL CHRG, 12.8 V (5 m 47.5 s on battery) |
| 20:10:39 | ups-mqtt: power restored; `outage` cleared, then retained `last_outage` 19:04:49Z → 19:10:39Z, 350 s |

Link, internet, DNS and the broker connection held in every 1 s sample; no
publish timed out; no undervoltage. Start and end each lag the UPS by under
one poll (7 s and 9 s), so `last_outage` overstates by 2.5 s. Every `state`
arrived at a 10 s interval.

### 4. Clean cut through the local bridge (`4-bridged-cut/`)

The fixes deployed: poll 10 s, sample 2 s, ups-mqtt publishing to the Pi's
local Mosquitto (`127.0.0.1`), bridged to the main broker; telemetry at
QoS 0, events at QoS 1; ups-mqtt's own reconnect, not paho's. Network gear
on battery outlets. Recorded on the main broker, where subscribers are.

| Time | Event |
|---|---|
| 20:38:19.8 | `upsc`: OB, 13.5 → 13.0 V |
| 20:38:21.0 | ups-mqtt: outage detected; `outage`, `computed/on_battery`, `state` on the main broker the same second |
| 20:38:21–20:41:31 | `outage` every 10 s, `outage_duration_secs` 0 → 189; battery 13.0 V at first, then 12.4–12.5 V |
| 20:41:32.1 | `upsc`: OL CHRG, 12.8 V (3 m 12.3 s on battery) |
| 20:41:33.1 | ups-mqtt: power restored; `outage` cleared, then retained `last_outage` 19:38:21Z → 19:41:33Z, 191 s, then `state` |

Detection lagged the UPS by 1.3 s at the start and 1.0 s at the end, against
up to a full 10 s poll before sampling (7 s and 9 s in test 3). Each publish
reached the main broker, through the bridge, within the second it was sent.
`state` arrived every 10 s, plus once at each transition, after which the
poll re-anchors on it. `outage.json` was first seen in the state directory
in the 20:38:22 sample and was gone by 20:41:33.

Link, internet, name resolution and the 5 port-1883 connections held in
every sample (about one a second); the bridge stayed connected (one retained
`sites/site/bridge 1`, never repeated) and the site's network state stayed
`ok`. ups-mqtt logged three lines (outage detected, power restored,
published `last_outage`) and nothing else: no publish timed
out, no reconnect. The kernel logged nothing, so no undervoltage.

Not covered: the network going down with the power, which is where the
bridge's queue matters (failure modes 1, 3 and 4 below). The bridged
replays (`TestBridgedReplay_*`) cover those until a cut does.

### 5. Run to empty (`5-run-to-empty/`)

Same config as test 4, mains left off until the UPS died. The Pi has no
upsmon shutdown, so it lost power uncleanly; it has no real-time clock, so
it booted on its last saved time. Recorded on the main broker from another
machine, so that side outlived the Pi; the Pi's own recorder stops at the
death.

| Time | Event |
|---|---|
| 21:36:53.0 | ups-mqtt: outage detected (the 1 s `upsc` sample shows OB, 13.1 V, at 21:36:53.5); `outage` on the main broker the same second |
| 21:36:53–22:57:47 | `outage` every 10 s, 487 of them, none missed; battery 12.1 V after 30 min, 11.8 V after 50, 11.2 V after 70 |
| 21:40:00, 22:48:35 | kernel: undervoltage, 4 s each |
| 22:54:17.6 | `upsc`: OB LB, 10.8 V, the first LB this UPS has reported. ups-mqtt published `outage` with `low_battery: true` the same second, 4 s after the poll before, and `outage.json` recorded it |
| 22:57:47 | last publish reaches the main broker: OB LB, 10.3 V, `outage_duration_secs` 4854 (the Pi's journal, read on it after the test, ends at 22:54:25; the rest was lost in the cut) |
| 22:58:35 | the bridge's notification `sites/site/bridge 0` on the main broker, 48 s after the last publish |
| 23:09:32 | mains back; the Pi boots with its clock about 16¾ min slow (it reads 22:52) |
| shortly after | ups-mqtt starts (22:53:50 by the slow clock): "outage started 21:36:53 ran the battery low and the host has rebooted since; it ended at boot"; clears `outage` on the local broker. Two more undervoltage events while booting on the flat battery |
| 23:11:20 | NTP: first clock sync |
| 23:11:28 | uplink up; bridge reconnects (`sites/site/bridge 1`) and forwards what it holds (below) |
| 23:11:29 | ups-mqtt: retained `last_outage` 20:36:53Z → 22:09:32Z (exactly boot), 5559 s, `low_battery_reached: true`, `spans_restart: true`, `clock_synced: true` |

ups-mqtt did what it is meant to on every step. The low battery went out at
once rather than at the next poll. After the reboot it ended the outage at
boot, since the record was from an earlier boot and had seen LB, and held
`last_outage` until the clock synced; the times in it come from the
monotonic clock and the boot time, so the 16¾ min clock error did not
reach them.

When the bridge came back, the main broker received, in the same second and
before the fresh values:
- the `outage` from 22:56:37 (OB LB), then its clear;
- three `state` from the new boot, stamped 21:54:00Z, 21:54:10Z and
  21:54:30Z by the unsynced clock, so they look 17 min old;
- test 4's `last_outage` four times, and the host-setup project's
  `network/outage` three times.

All were superseded within the second, and the retained values ended right:
`outage` empty, the new `last_outage`, a current `state`. The local broker
saves to disk every `autosave_interval` (300 s); its last save before the
death was at 22:56:38, a second after that stale `outage`, and the restored
snapshot was forwarded when the bridge connected. Its log shows a single
successful bridge connect, at 23:11:28, after attempts that failed while
the uplink's address was unreachable until Tailscale was up. ups-mqtt
published `last_outage` once; the copies are most likely the retained
replay that the 2026-10-14 Ethernet unplug shows the local broker queueing
again on connect attempts. The three pre-sync `state`, published at
22:54:00, 22:54:10 and 22:54:30 by the slow clock, are from around when the
host-setup project's monitor saw the local broker stop acknowledging (a
QoS 1 publish at 22:54:14 timed out, and its next only went through at
23:11:29). Every other `state` of that window was dropped, as QoS 0 with no
bridge connected should be; those three were probably published while a
connect attempt was pending, which the same test shows keeps them.
Consumers should judge `outage` and `state` by the payload's `timestamp`
and `clock_synced`, not by arrival order, across a power cycle.

### Failure modes found

Tests 1–3 exposed these; each now has a fix and a test.

1. Broker unreachable while the process is up (DNS, link, router boot):
   per-message 5 s timeouts stretch the poll from 10 s to ≈30 s, which
   skews outage start/end times. Fixed: after one publish times out the
   rest of the poll is skipped, and power changes are sampled every 2 s
   regardless (`TestLiveReplay_NetworkUnpowered`, `TestLiveReplay_DNSBroken`).
2. A broken connection that never recovers by itself and is not logged as
   such. Fixed: ups-mqtt detects the loss itself, logs it and reconnects,
   and replaces a connection whose publishes stall unacknowledged
   (`TestConnectionLost_ReconnectsAndSignals`,
   `TestPublish_WedgedStallReplacesConnection`).
3. paho's unbounded in-memory queue: hours of stale telemetry and events
   delivered in a burst on reconnect, in arbitrary order, so a retained
   topic can end on a stale value (including a stale `outage`). Fixed: paho's
   auto-reconnect is off and nothing is queued in the process; on reconnect
   current state is republished (`TestLiveReplay_*`). Behind the local
   bridge, telemetry goes at QoS 0 so mostly events queue (QoS 0 states
   published while a bridge connect is pending still do; see 2026-10-14)
   (`TestBridgedReplay_*`).
4. Network down for the whole outage: events only arrive after the fact;
   `last_outage` must still be right. Covered by the retained `last_outage`,
   built from the state file and the monotonic clock (`TestRestart*`,
   `TestBridgedReplay_NetworkUnpowered`); test 5 showed it live across a
   power cycle.

## 2026-10-14: network cut at the Pi (Ethernet unplugged)

Data: [`testdata/live-tests/2026-10-14/`](../testdata/live-tests/2026-10-14/).

### 1. Ethernet unplugged for three minutes (`1-ethernet-unplug/`)

Same config as 2026-10-13 tests 4 and 5, with `clock_synced` in `state`.
Mains stayed on; the Pi's Ethernet cable was pulled, then plugged back in.
This is failure mode 1 above for the bridge, not for ups-mqtt: ups-mqtt
publishes to the local broker, which stays reachable, so only the bridge
loses its uplink. The bridge has the uplink's name as its primary address
and its Tailscale IP as a fallback, `round_robin false`,
`restart_timeout 5 60`, `cleansession false`. The connect attempts below
are from the local broker's log, read by the host-setup project.

| Time | Event |
|---|---|
| 19:52:45 | last `state` to reach the main broker before the cut |
| 19:52:50.5 | kernel: eth0 link down (NetworkManager notices at 19:52:56.5) |
| 19:53:25 | the bridge's connection is gone on the Pi (6 port-1883 connections where there were 7) |
| 19:53:31, 19:53:48, 19:54:41, 19:55:16 | bridge connect attempts, alternating IP and name; all fail |
| 19:53:35 | `sites/site/bridge 0` on the main broker, 45 s after the cut |
| 19:55:43.7 | kernel: link up; DHCP lease 0.2 s later |
| 19:56:47 | bridge connects (on the IP), 63 s after the link came back, and forwards what it holds (below) |
| 19:56:53 | bridge drops the IP connection on finding the name reachable again |
| 19:56:59 | bridge connects on the name; the monitor reports `ok`, `state` every 10 s again |

ups-mqtt logged nothing and published `state` every 10 s throughout. The
UPS stayed on line (OL; CHRG dropped out for single samples a few times).
The main broker saw nothing from the Pi between 19:52:45 and 19:56:47,
about 4 minutes for a 2 m 53 s cut. The 63 s after the link came back
was a connect attempt from before it (19:55:16) still pending, then the
next attempt. The host-setup project has since lowered the cap to
`restart_timeout 5 30`, which shortens that wait but cannot remove it.

The burst at 19:56:47 held, interleaved and all with `clock_synced: true`:
- 9 `state` (and each one's 4 `computed/*`), stamped 18:53:35Z, 18:53:55Z,
  18:54:05Z, 18:54:15Z, 18:54:45Z, 18:55:25Z, 18:55:35Z, 18:55:45Z and
  18:56:45Z: each published around a connect attempt, while one was
  probably pending, which the local broker evidently queues for the
  bridge although telemetry is QoS 0. The other 15 publishes from
  18:52:55Z to 18:56:35Z were dropped, as they should be.
- 6 identical copies of the retained `last_outage` (2026-10-13 test 5's),
  and 2 more at 19:56:59; likewise 6 and 2 of the host-setup project's
  `network/last_outage`, and 8 of its `network/outage` (its `lan_down`,
  then `bridge_down` values). ups-mqtt did not publish them: the local
  broker queues its retained messages for the bridge again on connect
  attempts, though not exactly one copy per attempt. This is where
  test 5's repeats came from.

It ended right: the last `state` to arrive was current, and `last_outage`
unchanged. A consumer that orders `state` by its `timestamp` sees a sparse
but correct history of the cut, followed by the present.

## 2026-11-02: unplanned power cut, macOS host (CyberPower)

Data: [`testdata/live-tests/2026-11-02/`](../testdata/live-tests/2026-11-02/).

Not a planned test: a real cut overnight, pieced together the next morning
from what the hosts had kept. Two UPSes were on the cut circuit: the broker
host's APC, and the macOS host's CyberPower on USB. Config: poll 30 s, sample 2 s, QoS 1, broker by Tailscale name.

### 1. The cut (`1-power-cut/`)

| Time (UTC) | Event |
|---|---|
| 03:03:37.965 | macOS host: en0 `Link down … Cable Unplugged`; its DHCP address goes 0.1 s later |
| 03:03:39 | broker host's ups-mqtt: outage detected (upsmon agrees at 03:03:40) |
| 03:03:42 | macOS host's ups-mqtt: outage detected; the `outage` publish times out after 5 s |
| 03:04:00 | macOS host: MQTT connection lost; every reconnect fails from here on |
| 03:51:20.66 | broker host's upsmon: on line power |
| 03:51:20.848 | macOS host: kernel `terminateDevice: destroying 0x0764/… (CP1500EPFCLCD): hardware port disabled`; the UPS leaves the USB bus |
| 03:51:21 | broker host's ups-mqtt: power restored, `last_outage` published (2861 s, minimum charge 25 %) |
| 03:51:24 | macOS host: NUT polls fail `DATA-STALE`, and keep failing |
| 03:51:44 | macOS host: link back, DHCP requests its address |
| 03:52:02 | macOS host: reconnected after 48 min (51 attempts); republishes, but has no reading to send |

The router and the broker host stayed up: the broker host kept reaching
the gateway and Tailscale's relays throughout, and logged the macOS host's
node disappearing from them at 03:04:04. Something between the macOS host
and the router had no battery behind it: a switch on mains, found the next
morning. The macOS host's Wi-Fi was off, so it had nothing to fall back on.

What subscribers saw: `broker-ups` reported the cut exactly. `site-ups`
reported nothing at all. No `outage`, no `last_outage`. Its retained
`state` read `{"online":false,"timestamp":"2026-10-26T13:55:32Z"}`. That is
the LWT, set when the process connected six days earlier, and fired when the
broker lost the client at the start of the cut. Its timestamp is the
process's connect time, not the time it was fired.

### 2. Recovery (`2-usb-replug-and-switch/`)

The macOS host could not be rebooted, and its outage record was still
open: it saw the cut begin, then lost both the network and the UPS before
mains returned, so it never saw the end. Whenever the UPS came back it
would have closed the outage then, hours late; after a restart, at the
restart, with `spans_restart`. The record was removed and the daemon restarted on 2026-11-02,
and a `last_outage` was published by hand with the start the daemon saw
and the end the kernel gave (the second the port was disabled, which
matches the broker host's upsmon), 2857 s, no minimum charge: it was never
recorded.

On 2026-11-03:

| Time (UTC) | Event |
|---|---|
| 09:50:37 | UPS's USB lead unplugged and replugged at the Mac: enumerated again within seconds, on another port |
| 09:50:39 | ups-mqtt: `NUT answering again after 43962 failed samples`; the NUT driver reclaimed the device by itself |
| 09:55:31 | switch unplugged from mains, to move it onto the CyberPower; en0 down |
| 09:56:12.345 | kernel: `hardware port disabled` for the UPS again, and re-enumerated 5 ms later |
| 09:56:40 – 09:57:26 | en0 up, down 9 s later, up for good: the switch booting |
| 09:57:34 | ups-mqtt reconnected after 1 m 43 s and republished |

The second port disable came while the switch was plugged into the UPS,
so plugging a load into the UPS can trip it too, not only mains returning.
This time it came back by itself. The move is not yet proven by a cut:
the UPS reports `ups.load: 0` before and after, too coarse to see a switch.

### Failure modes found

1. **The host's network not on a battery.** The same as 2026-10-13
   test 1, on another host: ups-mqtt cannot report what it cannot send.
   Fixed in the hardware, not yet tested by a cut.
2. **macOS disables the UPS's USB port as power returns.** Not the
   long-uptime drop, which needs a reboot: a replug brings it back.
   `ups-doctor.sh` now reads the UPS's last USB event from the kernel log
   and says which it is (`scripts/test/ups-doctor_test.sh`). The log keeps
   those lines for about a day; after that it says it cannot tell, and
   advises the replug first anyway.
3. **An outage whose end nobody saw** — open. If the UPS becomes
   unreadable before mains returns, the tracker keeps the outage open
   until it can read the UPS again and ends it then, however late. Here it
   was put right by hand. Ending an outage when the UPS has been
   unreadable for long, or using another host's end, would need a decision
   about what the daemon may assume.

## 2026-11-18: unplanned run to empty, Pi host (Riello)

Data: [`testdata/live-tests/2026-11-18/`](../testdata/live-tests/2026-11-18/).

Not planned: mains failed in the evening and stayed off until the UPS died,
which makes this an unplanned repeat of 2026-10-13 test 5 on the same
hardware and config (poll 10 s, sample 2 s, behind the bridged local
broker). Recorded with `scripts/live-test.sh` from 11½ min in, from both
sides of the bridge: the main broker, and the Pi's local broker read over
ssh. The full UPS snapshots (`ups-host-listvar.log`) are new.

### 1. The run (`1-run-to-empty/`)

| Time (UTC) | Event |
|---|---|
| 21:54:42 | ups-mqtt: outage detected |
| 21:54:42–23:14:52 | `outage` every 10 s; none missed in the 412 recorded. Battery 12.4 V, 12.1 V after 30 min, 11.8 V after 50, 11.3 V after 70 |
| throughout | the driver reports `battery.charge` 3434 and `battery.runtime` 3308760 (38 days); both kept out of `state.variables`, `computed/` and `outage` |
| 22:19:59–23:12:24 | kernel: undervoltage, five times, 4–6 s each |
| 23:11:40.9 | `upsc`: OB LB at 10.8 V. ups-mqtt publishes `low_battery: true` at 23:11:42 |
| 23:14:52 | last publish reaches the main broker: OB LB, 10.3 V |
| ≈23:15:00 | the Pi loses power (its 1 s log ends at 23:14:42; pings fail from 23:15:03). The router evidently shares the UPS: the internet stayed up throughout the cut, and the router was still booting after the Pi had come back |
| 23:15:38 | `sites/site/bridge 0` on the main broker, 46 s after the last publish |
| 23:26:25 | mains back, the Pi boots, its clock set to 23:11:59 from the last saved time (14½ min slow) |
| ≈23:27:58 | ups-mqtt starts (23:13:33 by the slow clock): "outage started 21:54:42Z ran the battery low and the host has rebooted since; it ended at boot". Its start waited 20 s on Mosquitto, which stalled resolving the bridge's address while the router booted |
| 23:28:13.35 | NTP: first clock sync |
| 23:28:13.43 | the local broker drops every client, ups-mqtt among them, as having exceeded their keep-alive (below). ups-mqtt: `MQTT connection to tcp://127.0.0.1:1883 lost: EOF`, reconnected within the second, republished current state, and published `last_outage` |
| 23:28:30 | the router's uplink is back; the bridge reconnects and the main broker gets 32 messages for this site in one second (below) |

`last_outage`: 21:54:42Z → 23:26:25Z (exactly boot), 5503 s,
`low_battery_reached: true`, `spans_restart: true`, `clock_synced: true`,
no minimum charge (the only charge reading was impossible).

The battery behaved as it did five weeks earlier: 80 min 10 s from the cut
to the last publish (test 5: 80 min 54 s), LB at 10.8 V both times, and
3 min 11 s from LB to the last publish (test 5: 3 min 29 s).

### What the service did

Everything it is designed to do, and all of it right:
- An `outage` every poll for 80 minutes, and `low_battery: true` within
  1.1 s of the UPS raising LB. LB came at a scheduled poll, so this run
  does not show whether the at-once publish of a sample fired; test 5 does.
- The Riello's impossible readings never reached a consumer.
- The open outage survived the hard power-off in `outage.json`, which had
  recorded the LB. On boot the record was from an earlier boot and had seen
  LB, so the outage ended at boot, as designed.
- It held `last_outage` until the clock synced, 15 s after it started,
  and anchored the times on the monotonic clock and the boot time, so the
  14½ min clock error never reached them.
- It reconnected at once when the local broker dropped it, and
  republished, which put the retained topics right.

### What subscribers saw

**The death.** Nothing for 46 s, then `sites/site/bridge 0`. ups-mqtt's
LWT cannot fire at that point: it is registered with the local broker,
which died with the host. The retained `state` stayed on the last
snapshot, OB LB at 10.3 V, for 13½ minutes. As the README says, behind a
bridge `online:false` only covers the process dying. A consumer that sees
`bridge 0` after an OB LB `state` should take it that the site ran flat.

**The return.** The main broker got everything in the same second:

1. `bridge 1`;
2. the `last_outage` from 25 October, and the `outage` from 23:11:42
   (OB LB). These are the local broker's retained values as it last saved
   them before the death (`autosave_interval` 300 s), as in test 5;
3. the `outage` clear, then `state` `{"online":false,"timestamp":"2026-11-18T23:13:33Z"}`:
   ups-mqtt's LWT, fired when the local broker dropped it at the clock sync
   (below). Its timestamp is the process's connect time by the slow
   clock, so it is 14½ minutes wrong. Unlike a snapshot, it carries no `clock_synced`
   to say so;
4. the new `last_outage`, current `state` and `computed/*`;
5. second copies of 2–4, the old `last_outage` among them: the local
   broker's retained values queued again on connect attempts (2026-10-14);
6. a third copy of the new `last_outage`.

The last value of every topic was right. A consumer acting on arrival
order would have seen, within one second, an outage in progress, two
offline announcements and an older outage summary arrive after the newer
one. Order `outage` and `state` by `timestamp`, and `last_outage` by
`ended_at`.

No `state` from before the clock sync reached the main broker. In test 5
three did; this time the bridge was not connecting until after the sync,
and QoS 0 telemetry is not queued while it is down.

**The disconnect at the clock sync.** The local broker's log (root only)
says why every client was dropped at 23:28:13:

```
1795044493: Saving in-memory database to /var/lib/mosquitto//mosquitto.db.
1795044493: Client <weather client> has exceeded timeout, disconnecting.
1795044493: Client <ups-mqtt> has exceeded timeout, disconnecting.
1795044493: Client <network monitor> has exceeded timeout, disconnecting.
```

Mosquitto 2.0.21 times keep-alives (and its autosave) by the wall clock.
The clock stepped forward 14½ minutes, so every client looked silent for
14½ minutes, against an allowance of 1.5 × 30 s. The broker dropped all
of them and fired their wills. The step is the time the Pi spent without
power (its clock resumes from the last time it saved), so this will
happen after every reboot that follows a long cut. In test 5 the old client
logged nothing of the kind, and the broker's log of that day has since been
rotated away. But the host-setup project's monitor stalled until just after
that sync, which fits.

It cost nothing here: ups-mqtt reconnected within the second and
republished. But it is why a running process was announced `online:false`
on the main broker, with a timestamp 14½ minutes wrong.

### Failure modes found

1. **Nothing shuts the Pi down.** `nut-monitor` (upsmon) has failed at
   every boot since 2026-10-14: `Fatal error: insufficient power configured!
   … MINSUPPLIES … Edit your upsmon.conf`, i.e. no working `MONITOR` line.
   upsd is fine, so ups-mqtt kept working and nothing in MQTT shows it.
   Two hard power-offs in five weeks. The SD card survived both. The files
   being written lost their last 10–30 s and ended in NUL padding.
2. **ups-mqtt's offline announcement can carry an unsynced time and does
   not say so.** Both the LWT and the snapshot carry a `timestamp`, but
   only the snapshot carries `clock_synced`. After a long cut the LWT is
   all but certain to fire at the first clock sync (above), stamped with
   the unsynced time. Fixed: the LWT and the shutdown announcement carry
   `clock_synced` too (`TestFormatOffline_ClockSynced`,
   `TestPublishOffline_CarriesClockSynced`).
3. **ups-mqtt does not log low battery.** The journal of the dead boot
   ends at 23:12:30, 50 s after LB, and has no trace of it. Only
   `outage.json` and MQTT do. Fixed: its onset is logged with the battery
   voltage, and its end too, at most once a poll interval
   (`TestSample_LogsLowBattery*`).
4. **A replayed `last_outage` has nothing to order it by** except
   `ended_at`. A consumer that keeps the last to arrive was only right
   because a third copy of the new one came last. Open: document the
   `ended_at` rule, or give it a `timestamp`.
5. **The Pi sees undervoltage on battery.** Five 4–6 s events in 80 min
   (two in test 5); `vcgencmd get_throttled` reports 0x50000 (undervoltage
   and throttling have occurred). The Pi's power supply runs on the UPS's
   inverter output. Hardware, not this repo; it adds to the risk to the
   SD card.

### Shutting the Pi down safely

LB gave 3 min 11 s of warning here and 3 min 29 s in test 5, at this load.
That is plenty for a Pi to halt (well under 30 s), so upsmon's standard LB
shutdown is enough as the trigger. No timer is needed. A timer on
`ONBATT` would only cut the router off earlier: it shares the UPS.

The risk is the race. If mains returns after the Pi has halted but
before the battery runs out, the UPS never drops its output. A Pi 3 has
no power button and cannot wake itself, so it stays off and unreachable
until someone power-cycles it.

The NUT answer is to have the UPS cycle its output even then. upsmon
writes `POWERDOWNFLAG`, and at the very end of the halt (filesystems
read-only) NUT's shutdown hook tells the UPS to turn its output off after
`ups.delay.shutdown` and back on when mains is present. The Riello lists
`shutdown.return` ("Turn off the load and return when power is back"),
`ups.delay.shutdown` and `ups.delay.reboot` (both 5 s now; up to 3
digits), and `driver.flag.allow_killpower` (0 now). What is not known is
whether this UPS honours `shutdown.return` when mains is *already* back,
which is exactly the race. Some UPSes ignore it on line. That needs
testing with someone on site:

1. on mains, `upsmon -c fsd`: the Pi halts, and the UPS must drop and
   restore its output within `ups.delay.shutdown` + `ups.delay.reboot` +
   a few seconds. The router cycles too.
2. a cut with mains restored after the halt but before the battery dies:
   the same must happen.

If the Riello fails test 1, `shutdown.return` cannot be trusted with the
race. The alternative is not to halt: on LB, stop what writes, sync,
remount read-only and wait. If mains returns, reboot; if not, the power
dies on a clean filesystem. That depends on nothing in the UPS, but it is
more work and needs its own tests.

Either way the upsmon config is host setup, outside this repo. The one
change here would be for `ups-doctor.sh` to report a failed `nut-monitor`.
