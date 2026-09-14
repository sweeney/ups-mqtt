# ups-mqtt

A Go daemon that polls a [Network UPS Tools](https://networkupstools.org/) (NUT) server and publishes UPS state to an MQTT broker. Runs as a systemd service on Linux or a LaunchAgent on macOS.

Tested against a **CyberPower CP1500EPFCLCD** (900 W, 1500 VA) and an **APC Back-UPS XS 1400U** (700 W, 1400 VA), but compatible with any UPS that NUT supports.

---

## What it publishes

Every poll cycle, ups-mqtt publishes two kinds of MQTT messages. When the UPS switches to battery a third, outage-specific topic is published as well.

The middle component of every topic is the **label** — a human-readable name set in config (e.g. `office-ups`, `network-ups`). It defaults to the NUT device name (`ups_name`) if no label is set.

The state topic is published on every poll whether or not anything changed, so it doubles as a heartbeat.

> **Raw NUT variables are no longer published as individual topics.**
> Earlier versions mirrored every variable `upsc` returns onto its own retained
> topic (`ups/office-ups/battery/charge` → `"100"`, and so on) — around 46 topics
> per poll. They duplicated, value for value, what the state topic already carries
> as JSON, and measurement showed 88% of them never changed even across a full
> power cut. The values are unchanged and still available: read them from
> `variables` in the [state topic](#2-json-state-topic).
>
> Note that retained topics persist until overwritten, so an upgraded deployment
> will leave its old variable topics frozen at their last value on the broker
> until they are cleared.

### 1. Computed metrics

A set of derived values published under `computed/`:

| Topic | Formula | Example |
|-------|---------|---------|
| `…/computed/load_watts` | `ups.load / 100 × ups.realpower.nominal` | `72` |
| `…/computed/battery_runtime_mins` | `battery.runtime / 60` | `82` |
| `…/computed/battery_runtime_hours` | `battery.runtime / 3600` | `1.37` |
| `…/computed/on_battery` | `ups.status` contains token `OB` | `false` |
| `…/computed/low_battery` | `ups.status` contains token `LB` | `false` |
| `…/computed/status_display` | Human-readable decoded status | `"Online"` |
| `…/computed/input_voltage_deviation_pct` | `(voltage − nominal) / nominal × 100` | `5.22` |

Status tokens are decoded: `OL`→Online, `OB`→On Battery, `LB`→Low Battery, `CHRG`→Charging, `DISCHRG`→Discharging, `RB`→Replace Battery, and so on.

> **Note on `load_watts` accuracy at low load.** The CyberPower CP1500EPFCLCD's HID
> firmware only reports `ups.load` as a **whole integer percent** of its 900 W rating —
> i.e. a resolution of **9 W per step**. It exposes no `output.current` or actual-power
> field, so `load_watts` can only ever be `load% × 900`. Below ~9 W of real draw the meter
> rounds to **0%**, and a light load near one step dithers between 0% and 2% (0 W / 18 W).
> So a reading of **0 W is expected for small loads and does not mean nothing is plugged in**.
> Cross-checked 2026-06-03: the host Mac mini (M4) drew ~0.24 W at the SoC (a few watts at
> the wall) while the UPS reported 0% — the load is simply below this oversized unit's
> measurement floor. For true low-wattage figures, use an inline energy monitor (e.g. a
> Shelly PM) on the UPS output. A spot-check script lives at `scripts/soc-power-probe.sh`.

### 2. JSON state topic

A single combined JSON snapshot on `{prefix}/{label}/state`, published every poll:

```json
{
  "timestamp": "2026-02-23T16:40:18Z",
  "ups_name": "office-ups",
  "variables": {
    "ups.status": "OL",
    "ups.load": "8",
    "battery.charge": "100",
    "battery.runtime": "4920",
    "input.voltage": "242"
  },
  "computed": {
    "load_watts": 72,
    "battery_runtime_mins": 82,
    "battery_runtime_hours": 1.37,
    "on_battery": false,
    "low_battery": false,
    "status_display": "Online",
    "input_voltage_deviation_pct": 5.22
  }
}
```

`variables` carries every NUT variable except two families, which are excluded
because they describe NUT itself or hardware the device does not have:

| Excluded | Why |
|----------|-----|
| `driver.*` | NUT's own build and configuration metadata — `driver.version`, `driver.parameter.*` (the contents of `ups.conf` echoed back), `driver.debug`. Says nothing about the UPS. `driver.state` is also the noisiest value in the system, flapping between `updateinfo` and `quiet`. |
| `output.L2.*`, `output.L3.*` | Phantom phases. Single-phase UPSes report them as permanently zero. `output.L1.*` is kept. |

This is a denylist rather than an allowlist, so variables from an unfamiliar UPS
still reach subscribers — different devices expose markedly different variable
sets, and an allowlist would silently swallow anything unanticipated.

Values that a UPS does not report are **absent**, not zero. A device with no
`battery.runtime` publishes no `battery_runtime_mins`, rather than `0` — which a
subscriber would otherwise read as a flat battery. See
[Absent versus zero](#absent-versus-zero).

### 3. Outage topic

When the UPS switches to battery (`ups.status` contains `OB`), a call-to-action message is published to `{prefix}/{label}/outage` on every poll:

```json
{
  "timestamp": "2026-02-23T16:40:38Z",
  "ups_name": "office-ups",
  "outage_started_at": "2026-02-23T16:40:28Z",
  "outage_duration_secs": 10,
  "status": "OB DISCHRG",
  "status_display": "On Battery, Discharging",
  "battery_charge_pct": 100,
  "battery_runtime_secs": 4090,
  "battery_runtime_mins": 68.17,
  "estimated_depletion_at": "2026-02-23T17:48:25Z",
  "load_watts": 72,
  "low_battery": false
}
```

| Field | Meaning |
|-------|---------|
| `outage_started_at` | When the OB condition was first detected (fixed for the duration of the outage) |
| `outage_duration_secs` | Seconds elapsed since `outage_started_at`; increases each poll |
| `estimated_depletion_at` | `timestamp` + `battery_runtime_secs` — the key field for graceful-shutdown decisions |
| `low_battery` | `true` when `ups.status` contains `LB` |

The message is always published **retained** so a subscriber that connects mid-outage receives it immediately. When mains power is restored, an empty retained payload is published to the same topic, clearing the retained copy from the broker. Subscribers should treat an empty or absent payload as "no active outage".

All other messages are published with configurable QoS and retain flag. An LWT (last will and testament) of `{"online":false,"timestamp":"…"}` is registered at startup so MQTT subscribers see the device go offline immediately if the daemon dies unexpectedly.

---

## Configuration

Configuration is TOML, with environment variable overrides for all values. On startup the daemon looks for a config file at the path given by `--config` (default `/etc/ups-mqtt/config.toml`), falling back to `./config.toml` if the primary path doesn't exist.

```toml
[nut]
host          = "localhost"   # upsd hostname
port          = 3493          # upsd port
username      = ""            # leave empty if auth not configured
password      = ""
ups_name      = "cyberpower"  # name as shown in upsc -l
label         = "network-ups" # optional: MQTT topic name; defaults to ups_name
poll_interval = "30s"

[mqtt]
broker        = "tcp://localhost:1883"  # use "ssl://" for TLS
username      = ""
password      = ""
client_id     = "ups-mqtt"             # must be unique per broker when running multiple instances
topic_prefix  = "ups"
retained      = true
qos           = 1
tls_ca_cert   = ""                     # path to custom CA cert; empty = system CAs
```

The `label` field decouples the MQTT topic name from the NUT device identifier. This lets you use meaningful names like `office-ups` or `network-ups` independently of what the device is called in `ups.conf`.

If you run multiple instances against the same broker, each must have a distinct `client_id`. Duplicate IDs cause the broker to disconnect the older client when a new one connects.

### Environment variable overrides

Every field has a `UPS_MQTT_` override (useful for Docker / secrets):

| Variable | Field |
|----------|-------|
| `UPS_MQTT_NUT_HOST` | `nut.host` |
| `UPS_MQTT_NUT_PORT` | `nut.port` |
| `UPS_MQTT_NUT_USERNAME` | `nut.username` |
| `UPS_MQTT_NUT_PASSWORD` | `nut.password` |
| `UPS_MQTT_NUT_UPS_NAME` | `nut.ups_name` |
| `UPS_MQTT_NUT_LABEL` | `nut.label` |
| `UPS_MQTT_NUT_POLL_INTERVAL` | `nut.poll_interval` |
| `UPS_MQTT_MQTT_BROKER` | `mqtt.broker` |
| `UPS_MQTT_MQTT_USERNAME` | `mqtt.username` |
| `UPS_MQTT_MQTT_PASSWORD` | `mqtt.password` |
| `UPS_MQTT_MQTT_CLIENT_ID` | `mqtt.client_id` |
| `UPS_MQTT_MQTT_TOPIC_PREFIX` | `mqtt.topic_prefix` |
| `UPS_MQTT_MQTT_RETAINED` | `mqtt.retained` |
| `UPS_MQTT_MQTT_QOS` | `mqtt.qos` |
| `UPS_MQTT_MQTT_TLS_CA_CERT` | `mqtt.tls_ca_cert` |

Invalid values (e.g. a non-numeric port) are logged and ignored, leaving the default in place.

---

## Installation

### Prerequisites

- NUT installed and `upsd` running
- MQTT broker reachable (e.g. Mosquitto)
- Go 1.21+ on your build machine (not required on the target)

### macOS (LaunchAgent)

#### 1. Install and configure NUT

```bash
brew install nut
```

NUT config lives in `/opt/homebrew/etc/nut/`. Copy the example and edit the UPS name to match your device:

```bash
cp nut/ups.conf.example /opt/homebrew/etc/nut/ups.conf
```

The critical setting is `interrupt_pipe_no_events_tolerance = 10` — see [USB HID driver staleness](#usb-hid-driver-staleness) below for why this is essential.

Install the NUT LaunchDaemons (driver + data server). Both must run as system daemons (root) because `libusb` requires root to claim a USB HID device on macOS:

```bash
sudo cp nut/org.nut.usbhid-ups-cyberpower.plist.example /Library/LaunchDaemons/org.nut.usbhid-ups-cyberpower.plist
sudo cp nut/org.nut.upsd.plist.example                 /Library/LaunchDaemons/org.nut.upsd.plist

sudo launchctl bootstrap system /Library/LaunchDaemons/org.nut.usbhid-ups-cyberpower.plist
sudo launchctl bootstrap system /Library/LaunchDaemons/org.nut.upsd.plist
```

Verify NUT is talking to the UPS:

```bash
upsc cyberpower@localhost
```

NUT daemon logs:

```bash
tail -f /opt/homebrew/var/log/nut-driver.log
tail -f /opt/homebrew/var/log/nut-upsd.log
```

#### 2. Install ups-mqtt

```bash
./install-macos.sh
```

The script:
1. Builds a native binary for the detected architecture (arm64 or amd64)
2. Installs it inside `~/Applications/ups-mqtt.app` — required for macOS to track the app in TCC and prompt for Local Network privacy permission
3. Ad-hoc code-signs the bundle with identifier `net.swee.ups-mqtt`
4. Copies config to `/etc/ups-mqtt/config.toml` (sudo, first install only)
5. Installs and loads a LaunchAgent at `~/Library/LaunchAgents/net.swee.ups-mqtt.plist`

##### Local Network permission

ups-mqtt runs as a **LaunchAgent** (user session), not a LaunchDaemon. macOS enforces Local Network privacy (TCC) per-user session — a system daemon has no user context to prompt against, so the network connection is silently blocked.

On first install, run the binary once from the terminal to trigger the permission prompt:

```bash
~/Applications/ups-mqtt.app/Contents/MacOS/ups-mqtt --config /etc/ups-mqtt/config.toml
```

**The grant is tied to the binary's code hash, which changes on every build.**
An ad-hoc signature has no stable identity, so after each update macOS treats
the app as a different program. The toggle in System Settings can still *appear*
enabled while the new binary is denied — it belongs to the previous hash.

Symptoms, from a real occurrence:

```
connecting to MQTT broker "tcp://192.168.1.x:1883": no route to host
```

despite the broker being reachable from a shell, and the same binary working
when run from a terminal (which inherits the terminal's own grant). Confirmed
in the system log as a policy block rather than a routing failure — the flow is
killed at `SYN_SENT` with `reason: NECP`, and `errno 65` is `EHOSTUNREACH`:

```
process: ups-mqtt  t_state: SYN_SENT  error: 65  reason: NECP
UserEventAgent [com.apple.networkextension:] LocalNetwork: found bundle id net.swee.ups-mqtt
```

The fix is to toggle the permission off and on in **System Settings → Privacy &
Security → Local Network**, which re-keys it to the current binary.

**Better: avoid the gate entirely.** Point the broker at a Tailscale name rather
than a LAN address:

```toml
broker = "tcp://<host>:1883"    # a MagicDNS name, not 192.168.x.x
```

macOS does not classify a `utun` route as local network, so the permission never
applies. Verified: a fresh build connected immediately over Tailscale where the
same build was blocked on a LAN address. This removes the problem from every
future macOS deploy.

Re-running `install-macos.sh` is safe: it replaces the binary and reloads the service without touching the config.

```bash
# Stop / start
launchctl bootout  gui/$(id -u)/net.swee.ups-mqtt
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/net.swee.ups-mqtt.plist

# Logs
tail -f ~/Library/Logs/ups-mqtt.log
```

##### Rolling back a macOS deploy

Each update archives the outgoing binary to `~/Applications/ups-mqtt-versions/`,
keeping the last three. To undo the most recent one:

```bash
./install-macos.sh rollback
```

That restores the newest archived binary into the bundle, re-signs it, and
restarts the service.

The Linux path gets versioning for free — `deploy.sh` uploads a timestamped
binary and repoints a symlink, so rolling back is just moving the symlink. The
bundle cannot work that way: `codesign` and macOS's privacy database identify
the app by the real executable at `Contents/MacOS/`, and pointing that at a
symlink breaks the Local Network grant. So the outgoing binary is copied aside
before being overwritten instead.

> **Re-signing always reissues the Local Network grant**, on both update and
> rollback, because the ad-hoc signature's code hash changes with every build.
> That is invisible when the broker is reached over Tailscale — macOS does not
> classify a `utun` route as local network — but with a LAN broker address the
> daemon will fail with `no route to host` until the permission is re-approved
> in System Settings. See [Local Network permission](#local-network-permission).

#### USB HID driver staleness

On macOS, the `usbhid-ups` driver can silently stop receiving USB HID interrupt events from the device. NUT reports `Error: Data stale` and never recovers on its own. **The fix is `interrupt_pipe_no_events_tolerance = 10` in `ups.conf`** — it tells the driver to reconnect to the USB device after N consecutive polls with no events. At NUT's default 2 s poll interval that's ~20 s to auto-recover.

This is a macOS-specific issue with the `usbhid-ups` driver, not specific to any UPS brand — it affects APC, CyberPower, and others alike. The setting must be kept in `ups.conf` regardless of which device is connected.

This setting is already present in `nut/ups.conf.example`. Without it, the driver will silently stall and the only recovery is a manual restart.

#### When the UPS stops reporting

Start with the doctor script — it identifies which link in the chain has failed
before changing anything:

```bash
sudo ./scripts/ups-doctor.sh diagnose   # report only
sudo ./scripts/ups-doctor.sh            # diagnose, then repair what is safe
```

`Error: Data stale` has more than one cause and they need opposite remedies — a
device that has vanished from the USB bus needs a reboot, while one that is
present but has hung its USB interface needs the lead unplugged and replugged,
and will *not* be fixed by rebooting. See [`scripts/README.md`](scripts/README.md)
for how to tell them apart.

Restarting the driver is not free: while it holds the USB device nothing else can
claim it, so a restart releases the device and a wedged device may not be
reclaimable afterwards — leaving the driver down rather than merely stale. Check
the USB layer first. And **do not** kill the driver process directly; launchd
respawns it before macOS has released the device and it fails with `No matching
HID UPS found`.

`scripts/restart-nut-driver.sh` remains available if you only want to bounce the
driver.

### Linux (systemd)

```bash
# 1. Copy and edit the config
scp config.toml.example user@host:/tmp/ups-mqtt.toml
ssh user@host 'sudo mkdir -p /etc/ups-mqtt && sudo mv /tmp/ups-mqtt.toml /etc/ups-mqtt/config.toml'
# Edit /etc/ups-mqtt/config.toml on the target to match your NUT/MQTT setup.

# 2. Deploy
./deploy.sh user@host
```

Subsequent deployments:

```bash
./deploy.sh              # default host (TARGET in deploy.sh)
./deploy.sh user@host    # specific host
```

The script:
1. Detects the target architecture via SSH (`uname -m` → GOOS/GOARCH mapping)
2. Builds a stripped binary (`-ldflags="-s -w"`)
3. Uploads a timestamped versioned binary (`ups-mqtt-20260224-154321`)
4. Atomically swaps the symlink `/usr/local/bin/ups-mqtt` to point at the new binary
5. Restarts the service and waits briefly to confirm it's `active`
6. Prunes all but the three most recent versions from the remote host

#### Supported architectures

| Host `uname -m` | Builds as |
|-----------------|-----------|
| `x86_64` | `linux/amd64` |
| `aarch64` / `arm64` | `linux/arm64` |
| `armv7l` | `linux/arm` GOARM=7 |
| `armv6l` | `linux/arm` GOARM=6 (Pi Zero) |

#### systemd unit

The service file (`ups-mqtt.service`) is installed to `/etc/systemd/system/` via a symlink on first deploy. Key settings:

```ini
[Unit]
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=nobody
ExecStart=/usr/local/bin/ups-mqtt --config /etc/ups-mqtt/config.toml
Restart=on-failure
RestartSec=10s
```

The daemon handles `SIGTERM`/`SIGINT` gracefully: it publishes one final state snapshot before the offline announcement, then exits cleanly.

### Checking the service

```bash
# Linux
sudo systemctl status ups-mqtt
sudo journalctl -u ups-mqtt -f

# macOS
launchctl list | grep ups-mqtt
tail -f ~/Library/Logs/ups-mqtt.log

# Watch all MQTT topics
mosquitto_sub -h <broker> -t 'ups/#' -v
```

---

## Development

### Running locally

```bash
go run ./cmd/ups-mqtt/  # uses ./config.toml if present
```

With a NUT server and MQTT broker running locally (matching `config.toml`), you'll see all topics published to your broker every `poll_interval`.

### Building

```bash
go build ./...
go vet ./...
```

Cross-compile for a specific target:

```bash
GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o ups-mqtt-linux-amd64 ./cmd/ups-mqtt/
GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o ups-mqtt-linux-arm64 ./cmd/ups-mqtt/
GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w" -o ups-mqtt-darwin-arm64 ./cmd/ups-mqtt/
```

### Testing

```bash
go test -race ./...
go test -coverprofile=c.out ./internal/metrics/ && go tool cover -func=c.out
```

No real NUT server or MQTT broker needed — all tests use in-process fakes. `internal/metrics` is enforced at **100% statement coverage** by CI.

---

## Architecture & design

### Separation of concerns

```
cmd/ups-mqtt/main.go       Wiring: config → NUT → metrics → publisher → MQTT
internal/config/           Config struct, TOML loading, env overrides
internal/nut/              Poller interface + real NUT client
internal/metrics/          Pure computed metrics (no I/O)
internal/publisher/        Topic routing, JSON assembly, real MQTT publisher
```

### Why pure functions for metrics

`internal/metrics` contains only pure functions: given a `map[string]string` of NUT variables, return a `Metrics` struct. No I/O, no globals, no side effects. This means:

- **100% test coverage is achievable** — every branch exercisable with a map literal
- **Tests run instantly** — no process to start, no network, no hardware
- Adding a new metric touches exactly three places: the struct field, `Compute()`, and `AsTopicMap()`. CI rejects anything that drops coverage below 100%, so a missing `AsTopicMap()` entry is a compile-time + test failure.

### Absent versus zero

Not every UPS reports every variable, and the difference between "the device
cannot tell us" and "the value is zero" matters more than it might appear.

The Riello iPlug (`riello_usb`) reports no `battery.runtime` and no
`battery.charge` at all — confirmed across 168 consecutive samples spanning a
full power cut, where the variable set was identical on mains and on battery.
Published as a flat `0` to a **retained** topic, `battery_runtime_mins` reads
downstream as a battery with nothing left. The outage message was worse still:
with runtime unknown it dated `estimated_depletion_at` to the current instant,
announcing mid-outage that the battery was about to die.

So numeric fields in `metrics.Metrics` are `*float64`. A `nil` metric is omitted
from the state JSON and publishes no `computed/` topic at all, and
`OutageMessage` omits unknown battery fields and `estimated_depletion_at`
entirely. A real zero — a genuine 0% load — is still published as `0`; only
unknowns disappear.

This is why `computed/` is device-dependent: the CyberPower and APC units
publish all seven metrics, while the Riello publishes five.

### Why sentinel values are filtered numerically

`riello_usb` reports unpopulated bypass registers as `4095` (0xFFF, an all-ones
12-bit ADC word) and `409.50` (the same word scaled by ten). Published raw they
look like a 4 kV supply at 409 Hz, so `nut.VarsToMap` drops them.

The comparison is numeric, not textual, and that distinction is load-bearing.
The textual form is not stable: upsd sends `"409.50"`, and the previous
go.nut-based client decoded and re-rendered values through `%v`, turning it into
`"409.5"`. A string match passed every unit test written against `upsc` output
and failed silently against real hardware.

The client now keeps upsd's bytes verbatim, so `"409.50"` arrives as `"409.50"`
— but the numeric comparison stays, because drivers and NUT versions differ in
how many decimal places they emit.

Trailing zeros were the mild case. The same coercion turned `ups.beeper.status`
— a NUT enum of `enabled` / `disabled` / `muted` — into `"true"`, `"false"` or
`"muted"` depending on the reading. Raw values are now passed through exactly as
upsd sends them; `computed/` values remain normalised, because those are ours to
define rather than the device's.

### Dependency injection via interfaces

Two interfaces act as seams between business logic and infrastructure:

```go
type Poller interface {
    Poll() ([]Variable, error)
    Close() error
}

type Publisher interface {
    Publish(msg Message) error
    Close() error
}
```

`FakePoller` and `FakePublisher` ship in the production packages (`internal/nut`, `internal/publisher`) so all test packages — unit, integration, scenario — can share them without import cycles.

### Testing from real data

The integration scenario tests (`internal/integration_scenarios_test.go`) are built from live hardware captures:

**CyberPower CP1500EPFCLCD** — a ~2m40s power-cut event recorded on 2026-02-23, distilled into four NUT variable snapshots:

| Snapshot | `ups.status` | When |
|----------|-------------|------|
| `snapshotNormal` | `OL` | Steady-state on mains |
| `snapshotOnBattery` | `OB DISCHRG` | First poll after mains lost |
| `snapshotOLDischrg` | `OL DISCHRG` | Transitional: mains just restored |
| `snapshotCharging` | `OL CHRG` | Active charging |

**APC Back-UPS XS 1400U** — steady-state variables captured from a live device on 2026-05-15, used to verify cross-brand NUT variable compatibility.

`TestPowerCutSequence` replays the full CyberPower status machine using `FakePoller.Sequence`. Two real firmware quirks are tested explicitly:

- **Noisy battery charge readings** during discharge (100% → 79% → 82%)
- **`input.voltage = "0"`** for one poll during reconnect — the daemon reports −100% deviation without panicking

### Label and topic routing

The `label` field in config decouples the MQTT topic name from the NUT device identifier. `NUTConfig.EffectiveLabel()` returns the label if set, falling back to `ups_name`. All topic routing — including the LWT, outage topic, and JSON state — uses `EffectiveLabel()`, so renaming a device in MQTT never requires changes to the NUT configuration.

### Single source of truth for topic names

`metrics.Metrics` carries JSON struct tags. `AsTopicMap()` returns the same keys as MQTT topic suffixes. `StateMessage.Computed` embeds `metrics.Metrics` directly. The consequence: the JSON wire format and the per-topic names are defined once, in the struct. Adding a computed metric requires:

1. Add a field to `metrics.Metrics` (with JSON tag)
2. Compute it in `Compute()`
3. Return it from `AsTopicMap()`

That's it. The JSON state topic, the MQTT computed topic, and the test all pick it up automatically.

### Graceful shutdown

On `SIGTERM`/`SIGINT`:
1. Stop the poll ticker
2. Attempt one final `Poll()` — if it succeeds, publish a complete state snapshot
3. Publish `{"online":false,"timestamp":"…"}` to the state topic (retained)
4. Close NUT and MQTT connections
5. Exit 0

Subscribers see the final state before the offline announcement, so there's no gap in the timeline.

### NUT connection resilience

On startup, the daemon retries the NUT connection with exponential backoff (1 s → 2 s → 4 s → … capped at 60 s), each sleep interruptible by a signal. This means the service can be started before NUT is ready (e.g. at boot, race between systemd units) and will connect as soon as NUT is available.

If `Poll()` returns an error during normal operation (NUT restart, USB disconnect), the error is logged and the next tick retries automatically.

---

## CI

GitHub Actions runs two jobs on every push and pull request:

**`test`** — `go vet`, `go test -race -count=1 ./...`, coverage enforcement (metrics must be 100%), rich step summary with per-package coverage bars and slow-test list.

**`build`** — cross-compiles for linux/amd64, linux/arm64, and linux/armv6, reports binary sizes, uploads artifacts (90-day retention).
