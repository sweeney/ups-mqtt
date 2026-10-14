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
- the journal and kernel log.

It also subscribes to `ups/#` and `sites/#` on the broker. Behind the local bridge, point `--broker` at the main broker: that is where subscribers see the result.

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

### Failure modes to cover

1. Broker unreachable while the process is up (DNS, link, router boot):
   per-message 5 s timeouts stretch the poll from 10 s to ≈30 s, which
   skews outage start/end times.
2. A broken connection that never recovers by itself and is not logged as
   such.
3. paho's unbounded in-memory queue: hours of stale telemetry and events
   delivered in a burst on reconnect, in arbitrary order, so a retained
   topic can end on a stale value (including a stale `outage`).
4. Network down for the whole outage: events only arrive after the fact;
   `last_outage` must still be right.

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
