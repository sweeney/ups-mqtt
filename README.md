# ups-mqtt

A Go daemon that polls a [Network UPS Tools](https://networkupstools.org/) (NUT) server and publishes UPS state to an MQTT broker. Runs as a systemd service on Linux or a LaunchAgent on macOS.

Tested against a **CyberPower CP1500EPFCLCD** (900 W, 1500 VA) and an **APC Back-UPS XS 1400U** (700 W, 1400 VA), but compatible with any UPS that NUT supports.

---

## What it publishes

Every poll cycle, ups-mqtt publishes three kinds of MQTT messages. When the UPS switches to battery a fourth, outage-specific topic is published as well.

The middle component of every topic is the **label** — a human-readable name set in config (e.g. `office-ups`, `network-ups`). It defaults to the NUT device name (`ups_name`) if no label is set.

### 1. Raw NUT variables

Every variable that `upsc <upsname>` returns is published on its own topic, with dots replaced by slashes:

```
ups/office-ups/battery/charge          → "100"
ups/office-ups/battery/runtime         → "4920"
ups/office-ups/ups/status              → "OL"
ups/office-ups/ups/load                → "8"
ups/office-ups/input/voltage           → "242"
```

### 2. Computed metrics

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

### 3. JSON state topic

A single combined JSON snapshot on `{prefix}/{label}/state`:

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

### 4. Outage topic

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

ups-mqtt runs as a **LaunchAgent** (user session), not a LaunchDaemon. macOS enforces Local Network privacy (TCC) per-user session — a system daemon has no user context to prompt against, so the network connection is silently blocked.

On first install, run the binary once from the terminal to trigger the macOS Local Network permission prompt:

```bash
~/Applications/ups-mqtt.app/Contents/MacOS/ups-mqtt --config /etc/ups-mqtt/config.toml
```

Re-running `install-macos.sh` is safe: it replaces the binary and reloads the service without touching the config.

```bash
# Stop / start
launchctl bootout  gui/$(id -u)/net.swee.ups-mqtt
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/net.swee.ups-mqtt.plist

# Logs
tail -f ~/Library/Logs/ups-mqtt.log
```

#### USB HID driver staleness

On macOS, the `usbhid-ups` driver can silently stop receiving USB HID interrupt events from the device. NUT reports `Error: Data stale` and never recovers on its own. **The fix is `interrupt_pipe_no_events_tolerance = 10` in `ups.conf`** — it tells the driver to reconnect to the USB device after N consecutive polls with no events. At NUT's default 2 s poll interval that's ~20 s to auto-recover.

This is a macOS-specific issue with the `usbhid-ups` driver, not specific to any UPS brand — it affects APC, CyberPower, and others alike. The setting must be kept in `ups.conf` regardless of which device is connected.

This setting is already present in `nut/ups.conf.example`. Without it, the driver will silently stall and the only recovery is a manual restart.

#### Restarting the NUT driver

If the driver does need a manual restart:

```bash
sudo ./restart-nut-driver.sh
```

This does a clean `launchctl bootout` / `bootstrap` cycle and waits 5 s before checking `upsc`. **Do not** just kill the driver process — launchd will restart it, but if it was stopped via `bootout` first, macOS will have reclaimed the USB HID device and the driver will fail with `No matching HID UPS found`.

If you see `No matching HID UPS found` after a restart, unplug and replug the USB cable. This releases the macOS kernel HID driver's claim on the device so libusb can reclaim it.

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
./deploy.sh              # default host (sweeney@garibaldi)
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
