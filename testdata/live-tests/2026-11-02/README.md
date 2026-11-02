# Live captures, 2026-11-02

Captures from a real, unplanned power cut and its recovery the next
morning, written up in
[`docs/live-tests.md`](../../../docs/live-tests.md#2026-11-02-unplanned-power-cut-macos-host-cyberpower),
one directory per part.

Not recorded with `scripts/live-test.sh`: nobody was watching, so these were
gathered afterwards from what the hosts had kept. Two hosts are involved.
`ups-host` is the macOS host with the CyberPower on USB, label `site-ups`.
`broker` runs the main broker and has its own APC UPS (label `broker-ups`,
NUT name `apc`). Identifiers are replaced: Tailscale addresses are
`100.64.0.x`, LAN addresses `192.0.2.x`, the home directory `/Users/user`.
All times are UTC, the zone both hosts were in.

| File | What |
|---|---|
| `ups-host-ups-mqtt.log` | ups-mqtt's own log on the macOS host (the LaunchAgent's stdout) |
| `ups-host-unified.log` | `log show --style compact` lines from the kernel and `configd`: en0 link, DHCP address, and the UPS's USB arrivals and departures |
| `broker-journal.log` | `journalctl -o short-precise -u ups-mqtt -u nut-monitor` on the broker host |
| `broker-retained.log` | retained topics read from the broker with `mosquitto_sub -v` |

`1-power-cut/ups-host-unified.log` is not a fresh export. The unified log
dropped these lines within about 30 hours, so they are copied from what was
read on the morning of the cut. They are verbatim, but they are only the
lines that were read then, not every line from that window. In particular,
the en0 link-up line was not kept; the DHCP request at 03:51:44 follows it.

`1-power-cut/broker-retained.log` is the broker as found that morning, and
`2-usb-replug-and-switch/broker-retained.log` is the `last_outage` published
by hand afterwards (see the write-up). The broker host's tailscaled lines
are left out: they carry the peer's node-key prefix.
