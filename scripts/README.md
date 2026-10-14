# scripts/

Operational tooling — things you run **on a host** to inspect or repair a
running installation. Deployment lives in the repo root (`deploy.sh`,
`first-install.sh`, `install-macos.sh`, `remote-setup.sh`); nothing in here
builds or ships anything.

| Script | Purpose | Needs root |
|--------|---------|------------|
| [`ups-doctor.sh`](ups-doctor.sh) | Diagnose and repair the NUT → MQTT chain. Start here. | yes |
| [`restart-nut-driver.sh`](restart-nut-driver.sh) | Clean restart of the `usbhid-ups` driver alone. | yes |
| [`recover-ups-usb.sh`](recover-ups-usb.sh) | Forces a sleep/wake cycle to re-enumerate USB. Largely superseded — see below. | yes |
| [`soc-power-probe.sh`](soc-power-probe.sh) | Spot-check real power draw at the SoC, for when `ups.load` reads 0 %. | no |
| [`persist-journal.sh`](persist-journal.sh) | Keep the systemd journal across power loss on Raspberry Pi OS. | yes (`--check`: no) |
| [`install-unit.sh`](install-unit.sh) | Bring the installed systemd unit up to date with the repo's. | yes (`--check`: no) |
| [`install-sudoers.sh`](install-sudoers.sh) | Install the narrow passwordless-sudo rule `deploy.sh` needs. | yes, even `--check` |
| [`live-test.sh`](live-test.sh) | Record a live power-cut test (run from your machine; ships [`live-test-watch.sh`](live-test-watch.sh) to the host), then sanitise it for `testdata/`. | no |

## Keep the journal across a power cut

Raspberry Pi OS keeps the systemd journal in RAM
(`/usr/lib/systemd/journald.conf.d/40-rpi-volatile-storage.conf`), and ships no
syslog. On a host whose job is reporting power cuts, a power cut then erases
its own evidence.

```bash
./scripts/persist-journal.sh --check   # report only
sudo ./scripts/persist-journal.sh      # apply, with a confirmation prompt
```

It adds a drop-in that overrides the RPi default with `Storage=persistent`,
`SyncIntervalSec=10s` (the 5 min default would lose the run-up to a sudden
power loss) and `SystemMaxUse=64M`. Re-running it is a no-op. Tests:
`bash scripts/test/persist-journal_test.sh`.

## Update the systemd unit

`deploy.sh` replaces the binary but not the unit. Installing the unit is a
separate, deliberate step with its own diff and prompt. `deploy.sh` ships
`ups-mqtt.service` and this script to `~/ups-mqtt/setup/` and runs `--check`.
(Not to `~/ups-mqtt/` itself: hosts set up by an early `first-install.sh`
have the installed unit symlinked to `~/ups-mqtt/ups-mqtt.service`, so
writing the repo's unit there changed the live unit. Installing replaces such
a symlink with a file.) When the installed unit is out of date, the
deploy ends with the diff and the command to install it:

```bash
./deploy.sh --install-unit --site <site>
```

That ships this checkout's unit and the script again, then runs the script on
the host over `ssh -t`: the prompt needs a terminal, and without one the
script refuses rather than read an answer nobody was shown a prompt for. The
script keeps the `User=` the installed unit already has, checks the new
unit with `systemd-analyze verify`, and prompts before replacing it (`--yes`
skips the prompt). It then runs `daemon-reload` and restarts ups-mqtt if it is
running. Re-running it is a no-op. Tests:
`bash scripts/test/install-unit_test.sh` and `bash scripts/test/deploy_test.sh`.

## Narrow the deploy user's sudo

`deploy.sh` runs over ssh with no terminal, so the root commands it uses must
not ask for a password. `install-sudoers.sh` writes `/etc/sudoers.d/ups-mqtt`
allowing exactly those, and nothing else:

```
systemctl daemon-reload
systemctl restart ups-mqtt
tee /etc/ups-mqtt/config.toml     # only when a host has no config yet
```

Hosts installed before it have a rule allowing any `systemctl` and
`journalctl` command without a password. Any `systemctl` command is root
(`systemctl edit` writes a unit as root), and any `journalctl` command reads,
and can delete, every log on the host. To replace it:

```bash
./deploy.sh --install-sudoers --site <site>
```

sudo asks for your password, then the script shows the diff, checks the rule
with `visudo`, and prompts. It refuses if the sudoers configuration already
fails `visudo -c`; after installing it checks it again and puts the old rule
back if it now fails. It then lists any passwordless grant you still have
from elsewhere that leaves you root or able to read any log (`ALL`, or
`systemctl` or `journalctl` with no arguments or a wildcard). It does not
touch those; they belong to whatever installed them. The rule is only narrow
while the ups-mqtt unit is a root-owned file with a non-root `User=`, so it
warns if the unit is a symlink, writable by others, or runs as root. `first-install.sh` uses it for new hosts. Tests:
`bash scripts/test/install-sudoers_test.sh`; `deploy_test.sh` checks that
every `sudo` in `deploy.sh` is one the rule allows.

## Start with ups-doctor.sh

```bash
sudo ./scripts/ups-doctor.sh diagnose   # report only, changes nothing
sudo ./scripts/ups-doctor.sh            # diagnose, then repair what is safe
```

The chain has four links and each fails differently:

```
UPS ──USB──> usbhid-ups ──socket──> upsd ──TCP:3493──> ups-mqtt
```

`Error: Data stale` or `Driver not connected` can mean any of them, so the
script works out *which* link is broken before touching anything. Faults that
software can fix, it fixes; faults needing hands are reported plainly rather
than worked around. Exit codes: `0` healthy, `1` needs physical intervention,
`2` error.

## Why diagnosing first matters

**`Error: Data stale` has at least two causes, with opposite remedies.**

| Signature | Cause | Fix |
|---|---|---|
| `ioreg -p IOUSB -w0 -l \| grep '"idVendor" = 1892'` returns nothing | device gone from the bus | reboot |
| Device present, but the driver probe reports `Product: unknown` and takes ~300 ms to read the ID (vs ~1 ms for its neighbours) | the UPS's own USB interface has hung | unplug the USB lead, wait ~10 s, replug |

In the second case **a host reboot will not help**, because the wedge is in the
device rather than the host controller. Diagnosed and cleared without a reboot
on 2026-09-15.

To see this for yourself, probe with the real driver — it enumerates every USB
device and shows exactly where matching fails:

```bash
sudo /opt/homebrew/bin/usbhid-ups -a cyberpower -u root -DD -d1 2>&1 \
  | grep -E 'Checking device|VendorID|- Product:|failed to match|No appropriate'
```

A device that answers its vendor/product ID but not its string descriptors never
reaches the matching stage at all, so the log only says `No matching HID UPS
found` — which points away from the real problem.

## Restarting the driver is not free

While the NUT driver holds the USB device, nothing else can claim it. Restarting
the driver *releases* it — and if the device is wedged, the driver may then fail
to reclaim it, leaving you worse off than before (driver down, rather than merely
serving stale data). **Check the USB layer before restarting anything.**
`ups-doctor.sh` does this in the right order.

For the same reason, never `kill` the driver process directly: launchd respawns
it before macOS has released the device, and it fails with `No matching HID UPS
found`. Use `bootout` → wait for exit → `bootstrap`.

## recover-ups-usb.sh is now mostly a diagnostic

It schedules an RTC wake and forces a sleep/wake cycle to re-enumerate USB
without rebooting. Testing on 2026-07-01 showed that a confirmed deep sleep does
**not** clear a wedged USB controller on Apple Silicon, which keeps USB rails
partially powered. Kept because the scheduling and verification logic is useful,
but try `ups-doctor.sh` first.

## ups-mqtt recovers on its own

Once NUT is healthy again the daemon reconnects by itself, within one poll
interval, without a restart and without logging anything: `Poll` notices the
dead connection, closes it, re-dials and retries inside a single poll.

This was **not** true before 2026-09-15. The old go.nut-based client could not
close a socket, and a repair therefore had to be followed by restarting the
daemon by hand. If you find that instruction elsewhere, it is stale.

So do not restart it reflexively — a restart fires the Last Will, briefly
marking the device offline to subscribers, for no gain. Only intervene if the
daemon is not running at all:

```bash
launchctl kickstart -k gui/$(id -u)/net.swee.ups-mqtt   # macOS
sudo systemctl restart ups-mqtt                          # Linux
```

Verified across a `upsd` restart on all three hosts: the process ID was
unchanged, nothing was logged, and publishing never missed its cadence.
