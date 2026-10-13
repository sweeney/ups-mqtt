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

Production config at the time: poll 10 s, broker `tcp://broker:1883` by
Tailscale MagicDNS short name, paho auto-reconnect with default backoff,
5 s publish timeout, events at QoS 1.

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
