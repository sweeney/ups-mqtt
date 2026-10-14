# Live test captures, 2026-10-14

Raw captures from the live tests written up in
[`docs/live-tests.md`](../../../docs/live-tests.md#2026-10-14-network-cut-at-the-pi-ethernet-unplugged),
one directory per test.

Recorded with `scripts/live-test.sh` and laid out as 2026-10-13's tests 4
and 5 (see [their README](../2026-10-13/README.md)), with the same
identifiers replaced, plus the host's Tailscale address `100.64.0.2` and
other addresses in documentation ranges. `broker.log` holds only this UPS's
`ups/…` topics and the site's `sites/…`, including the host-setup project's
network monitor. `journal.log` leaves out tailscaled's path lines, which
carry node keys.

The retained `last_outage` and `network/last_outage` are from 2026-10-13
test 5; their times are shifted as that test's are, so they match it.
