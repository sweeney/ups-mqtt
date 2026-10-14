# Live test captures, 2026-10-13

Raw captures from the live tests written up in
[`docs/live-tests.md`](../../../docs/live-tests.md#2026-10-13-mains-cut-ups-feeding-the-pi-riello-iplug),
one directory per test.

Identifiers are replaced: the host is `ups-host`, the label `site-ups`, the
broker host `broker`, Tailscale addresses `100.64.0.x`, LAN addresses
`192.0.2.x`, the site `site`, a public endpoint `198.51.100.1`, the
Tailscale relay region `rgn`; NetworkManager connection IDs are zeroed.
Times in log lines are the Pi's local time (BST, UTC+1); payload
timestamps are UTC.

| File | What |
|---|---|
| `journal.log` | `journalctl -o short-precise` for ups-mqtt, NetworkManager, tailscaled (link/DNS lines only) |
| `kernel.log` | `journalctl -k` for the same window |
| `broker*.log` | `mosquitto_sub -v` on the broker, receive time first (`%I`) |
| `ups.log` | `upsc` `ups.status` and `battery.voltage`, sampled on the Pi |
| `net.log` | eth0 carrier, `ping 1.1.1.1`, contents of the state directory, every 1 s |
| `timeouts-per-minute.txt` | ups-mqtt publish timeouts per minute, standing in for the thousands of identical journal lines it replaces |

Test 1 has no `ups.log` or `net.log`; its `broker-state.log` has the
`variables` object elided. Tests 3 and 4 have no `kernel.log`: the kernel
logged nothing during them, and their `net.log` adds name resolution and
broker connections.

Test 4 was recorded with `scripts/live-test.sh`, so its files differ. Its
`ups.log` and `net.log` have a sample about every 1.05 s, not exactly every
1 s. `broker.log` adds the retain flag (`%r`) after the receive time, and holds
only this UPS's `ups/…` topics and the site's `sites/…` (bridge and network
state), not the broker's other UPSes. The flag is 1 only for the retained
copies delivered when the subscriber connected; everything after arrived
live, so shows 0, retained or not. `resolv.log` is `resolv.conf` at the
start; it did not change. Apart from tailscaled path lines, removed because
they carry node keys, only ups-mqtt logged anything, and the kernel nothing.

Test 5 is recorded the same way as test 4, without
`sites/site/network/state` in `broker.log` (the host-setup project's network
monitor, every 10 s, not needed here). The Pi's files stop when it lost
power (`ups.log` at 22:57:40, `net.log` at 22:57:45); null bytes the power
loss left at their ends are removed. `journal.log` is what the recorder took
from the Pi's journal before the death (the journal itself, read later, ends
at 22:54:25; the rest was lost); `journal-after-boot.log` is the next boot's
for ups-mqtt, mosquitto, NTP, the NUT driver and server, and kernel
undervoltage, read after the test. Its times before 23:11:20 are the
unsynced clock's, about 16¾ min slow, as are mosquitto's own epoch stamps.
The NTP server is `203.0.113.123`.
