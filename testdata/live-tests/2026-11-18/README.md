# Live captures, 2026-11-18

A real, unplanned power cut that ran the UPS flat, written up in
[`docs/live-tests.md`](../../../docs/live-tests.md#2026-11-18-unplanned-run-to-empty-pi-host-riello).
Recording began 11½ min into the cut and covers the death, the reboot and
the bridge reconnecting.

`ups-host` is the Pi with the Riello iPlug on USB (label `site-ups`, NUT
name `iplug`), publishing to a local broker that bridges to `broker`, the
main broker. Identifiers are replaced: Tailscale addresses are
`100.64.0.x`, LAN addresses `192.0.2.x`, public ones `198.51.100.x` and
`203.0.113.x`, machine, boot and connection IDs zeroed. All times are UTC.

| File | What |
|---|---|
| `ups-host-listvar.log` | upsd's `LIST VAR iplug` reply verbatim (wire format, as `internal/nut/testdata`), each headed `=== <time>`. One every 10 s until 23:10 (thinned from the 2 s recording by elapsed time, so there are no gaps), then every 2 s until the death. `TestReplay_RunToEmpty_2026_11_18` (`cmd/ups-mqtt`) replays it through the daemon |
| `ups-host-ups.log` | `ups.status` and `battery.voltage` from `upsc`, every 1 s |
| `ups-host-net.log` | link carrier, internet, broker-name lookup, MQTT connections, ups-mqtt's state directory, every 1 s (`dns=FAIL` is an artefact: the broker was given as a bare address, which has no reverse name) |
| `ups-host-resolv.log` | `/etc/resolv.conf` |
| `ups-host-journal-before.log`, `ups-host-kernel-before.log` | the journal (ups-mqtt, mosquitto, NetworkManager, NUT) and kernel log of the boot that died |
| `ups-host-journal-after.log`, `ups-host-kernel-after.log` | the same for the boot after, 21:40–00:30 by the host's clock, which was 14½ min slow until 23:28:13 |
| `broker-main.log` | every message for this site on the main broker: `<arrival> <retain> <topic> <payload>` |
| `broker-local-via-ssh.log` | the Pi's local broker, read over ssh from another machine: `<arrival there, ms> <arrival> <retain> <qos> <topic> <payload>`, from 22:10 |
| `link.log` | a ping a second from that machine to the Pi |
| `broker-main-retained-next-morning.log` | the main broker's retained topics for this site the next morning |

The files the Pi wrote end 10–30 s before it lost power (`ups-host-ups.log`
at 23:14:42, the last snapshot at 23:14:28): what was still in the page
cache was lost, and the files ended in NUL padding, stripped here. The
journal of the dead boot ends at 23:12:30 with `SyncIntervalSec=10s`, so
it most likely lost nothing: ups-mqtt did not log low battery then.

The snapshots were taken by a stopgap loop speaking the upsd protocol
directly, started before `scripts/live-test-watch.sh` recorded them; its
headers end `+00:00` where the script's end `Z`. The Pi's tailscaled lines
are left out: they carry peers' node-key prefixes.
