# Live test captures, 2026-10-13

Raw captures from the live tests written up in
[`docs/live-tests.md`](../../../docs/live-tests.md#2026-10-13-mains-cut-ups-feeding-the-pi-riello-iplug),
one directory per test.

Identifiers are replaced: the host is `ups-host`, the label `site-ups`, the
broker host `broker`, Tailscale addresses `100.64.0.x`, LAN addresses
`192.0.2.x`. Times in log lines are the Pi's local time (BST, UTC+1);
payload timestamps are UTC.

| File | What |
|---|---|
| `journal.log` | `journalctl -o short-precise` for ups-mqtt, NetworkManager, tailscaled (link/DNS lines only) |
| `kernel.log` | `journalctl -k` for the same window |
| `broker*.log` | `mosquitto_sub -v` on the broker, receive time first (`%I`) |
| `ups.log` | `upsc` `ups.status` and `battery.voltage`, sampled on the Pi |
| `net.log` | eth0 carrier, `ping 1.1.1.1`, contents of the state directory, every 1 s |
| `timeouts-per-minute.txt` | ups-mqtt publish timeouts per minute, standing in for the thousands of identical journal lines it replaces |

Test 1 has no `ups.log` or `net.log`; its `broker-state.log` has the
`variables` object elided. Test 3 has no `kernel.log`: the kernel logged
nothing during it, and its `net.log` adds name resolution and broker
connections.
