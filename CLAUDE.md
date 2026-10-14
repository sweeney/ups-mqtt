# ups-mqtt

Go daemon that polls a NUT (Network UPS Tools) server and publishes UPS state to MQTT.
Devices: CyberPower CP1500EPFCLCD, APC Back-UPS XS 1400U, Riello iPlug.
Runs as a systemd service (Linux) or a LaunchAgent (macOS).

## Module

```
github.com/sweeney/ups-mqtt
```

## Key dependencies

| Package | Role |
|---------|------|
| `github.com/eclipse/paho.mqtt.golang` | MQTT client |
| `github.com/BurntSushi/toml` | TOML config parsing |

## Layout

```
cmd/ups-mqtt/main.go           entry point, poll loop, graceful shutdown
internal/outages/              outage tracking across restarts, last_outage, clock
internal/config/config.go      Config + TOML loader + env overrides
internal/nut/                  Poller interface, real client, FakePoller
internal/metrics/              pure computed metrics (100% test coverage)
internal/publisher/            Publisher interface, topic routing, JSON, FakePublisher
internal/integration_test.go   end-to-end: FakePoller → metrics → FakePublisher
internal/integration_scenarios_test.go  replays captured power cuts (FakePoller.Sequence)
internal/nut/testdata/         wire-format fixtures from every real device
internal/retry/                connect with capped backoff and sparing failure logs
internal/mqtttest/             real Mosquitto, fault-injecting TCP proxy, recorder (tests only)
internal/mqtttest/bridge/      mirror of the production local-broker and bridge config
docs/live-tests.md             write-ups of live power-cut tests on real hardware (how: scripts/live-test.sh)
testdata/live-tests/<date>/    their raw captures (sanitised), the source for fixtures
scripts/                       host tooling (ups-doctor, install-unit, install-sudoers,
                               persist-journal, live-test); tests in scripts/test/
deploy.sh, first-install.sh    build and ship to a Linux host: --site NAME (deploy-sites.sh
                               reads the gitignored sites file; sites.example) or --host;
                               deploy.sh --check reports first, read-only
remote-setup.sh                the one sudo step first-install.sh prints
install-macos.sh               macOS LaunchAgent install (net.swee.ups-mqtt.plist)
ups-mqtt.service               systemd unit; config.toml.example; nut/ (macOS NUT examples)
```

## Test / build commands

```bash
go test -race ./...                             # all tests
go test -coverprofile=c.out ./internal/metrics/ # must be 100%
go vet ./...                                    # must be silent
go build ./...                                  # clean compile
for t in scripts/test/*_test.sh; do bash "$t"; done   # script tests (CI runs them)
```

## Conventions

- `internal/metrics` coverage must remain **100%** — CI enforces this.
- `FakePoller` and `FakePublisher` follow boiler-sensor conventions: exported
  fields (`Variables`, `Err`, `CallCount`, `Closed`, `Messages`, `PublishError`,
  plus `FailTopic`/`Failed` on FakePublisher for simulating a partially degraded
  link), a `Reset()` method, and `Find(topic)` / `IndexOf(topic)` helpers on
  FakePublisher — `IndexOf` is how publish ordering is asserted.
  `FakePoller.Sequence` replays successive snapshots, and
  `FakePublisher.Retained(topic)` gives what a broker would hold retained.
- The middle topic segment is the **label** (`NUTConfig.EffectiveLabel()`), which
  falls back to `ups_name`. Use it for topic routing; `ups_name` is only for the
  NUT device lookup.
- Telemetry is only two things, published every poll: computed metrics under
  `{prefix}/{label}/computed/`, and the combined state JSON on
  `{prefix}/{label}/state`. The event topics are below. Raw NUT variables are
  **not** published as individual topics — they duplicated the state payload
  and nothing read them. They travel inside `state.variables`.
- `{prefix}/{label}/outage` is published while on battery and cleared with an
  empty retained payload on recovery. It goes out **before** the telemetry on
  every poll and is never suppressed by a telemetry failure; a power cut is the
  one event this daemon exists to report. A fresh process does not know
  whether the topic is clear (a predecessor may have died mid-outage, e.g. the
  host lost power), so the first poll on mains clears it unconditionally.
  `TestRestartAfterOutage_*` replays that against the Riello fixture with
  `FakePublisher.Retained` standing in for the broker.
- QoS is per message, not per client: telemetry (`state`, `computed/*`) goes at
  `mqtt.telemetry_qos`; events (`outage` and its clear, `last_outage`, the
  offline announcement, the LWT) at `mqtt.event_qos`. Both fall back to
  `mqtt.qos`. Behind a bridged local broker, QoS 1 telemetry would fill the
  bridge's capped queue and get the outage dropped. A new publish must pick its
  kind.
- Startup retries the MQTT connect, then the NUT connect, via
  `retry.Connect` (1 s doubling to 60 s; a signal interrupts both the wait
  and an attempt in progress; an unchanged failure is logged once, then every
  5 min). `NewMQTTPublisher` does not dial; it fails only on errors no
  retry can fix (bad CA path), which are fatal, as is a QoS > 2, rejected in
  `config.Load` because paho does not check it. A CONNACK refusal is retried.
- **Never turn paho's auto-reconnect back on.** While auto-reconnecting,
  `IsConnected()` is true and QoS 1 publishes are stored and later resent in
  map order: live, 6½ hours of backlog arrived shuffled in 2 s and every
  publish waited out its timeout (docs/live-tests.md). `MQTTPublisher`
  reconnects itself (clean session, so paho's store is emptied), publishes only
  while `IsConnectionOpen()`, stops waiting on further publishes once one has
  timed out until it is acked or the connection replaced (a stall past 2 ×
  (keep-alive + ping timeout), 80 s, replaces it), skips the clean DISCONNECT
  in `Close` when the last publish failed so the broker fires the LWT instead
  of the lost offline announcement, and signals `Reconnected()`, on which the
  poll loop republishes current state at once (re-sending the outage clear).
  `TestLiveReplay_*` replays the live tests against a real Mosquitto behind
  `mqtttest.Proxy`, which blackholes or drops the link; they need `mosquitto`
  installed (CI installs it, and fails rather than skips without it). Stopping
  is bounded (one NUT poll plus one publish timeout against a dead broker with
  upsd answering; `TestShutdown_Bounded*`; a hung upsd is not bounded: an
  in-flight sample plus a final poll, each two timeouts and a re-dial, can pass
  20 s) and the unit's `TimeoutStopSec=20s` relies on it: on a battery shutdown
  the UPS cuts power a fixed 70 s after the host halts. `TestBridgedReplay_*`
  run the same timelines through the production local-broker and bridge config,
  mirrored (identifiers removed) in `internal/mqtttest/bridge/` from the
  separate host-setup project; `mqtttest.StartBridged` fails if a substitution
  it makes no longer matches, and the mirror must be kept in step by hand.
- `{prefix}/{label}/last_outage` is a retained summary published **after** the
  outage clear when an outage ends; never cleared. `internal/outages.Tracker`
  owns it: it persists the open outage to `state_dir`, else `$STATE_DIRECTORY`
  (systemd's `StateDirectory=`, or the macOS plist) (`outage.json`, written on
  transitions only, with the kernel `boot_id`; boot ID and boot time are
  Linux-only, `clock_other.go`). After a restart on battery it resumes the
  outage, except when the record is from an earlier boot and saw low battery:
  that outage ran flat and ended at boot. An outage that ended while nothing
  was running is reported with `spans_restart`, ending at boot after a reboot,
  else at process start. Every event is anchored on the monotonic clock and
  turned into wall time only at publish, so a later NTP sync corrects it; a
  summary waits for sync (interim after `DefaultSyncWait`, corrected later).
  Tests use `outages.FakeClock` and `outages.MemoryStore`; `TestRestart*` in
  `cmd/ups-mqtt` replays the scenarios end to end.
- The UPS is read every `nut.sample_interval` (default 2 s, capped at
  `poll_interval`) but published every `poll_interval`, plus at once when a
  sample sees on/off battery or low battery (`daemon.sample`), at most once
  per poll (the first low battery of an outage excepted), so flapping power
  cannot multiply it. The tracker follows every sample (`daemon.track`), so
  outage times are a sample's even when the publish was held back. Keep the
  publish rate: the Pi's bridged local broker queues one QoS 1 `outage` per
  publish while its uplink is down, and its cap was sized for 10 s polls.
- `state` is published every poll regardless of change, so it doubles as a
  heartbeat. It is also the LWT topic, so it carries two shapes: a full snapshot
  while alive, `{"online":false,…}` when the broker declares the client dead.
  The snapshot carries `clock_synced`, the clock's sync at that poll: after a
  power cut a bridged broker can forward states stamped by an unsynced clock
  among stale retained values (docs/live-tests.md, test 5).
- `publisher.noisyVariableFamilies` denylists `driver.*` and
  `output.L2.*`/`output.L3.*` from `state.variables`. Never filter
  `battery.charge` or `input.voltage` — statehouse's ups adapter reads both
  straight out of the payload.
- Unknown values are **absent, not zero**: numeric `metrics.Metrics` fields are
  `*float64`, and nil means the UPS cannot report it. A nil metric publishes no
  `computed/` topic and is omitted from the JSON. Never substitute 0 — a
  retained `battery_runtime_mins: 0` reads downstream as a flat battery.
  The same goes for a device that sends a constant 0 for something it does not
  measure: `nut.unimplementedVariables` drops those, keyed on `driver.name` and
  optionally the model, because a zero cannot be told from a reading by value.
  It currently covers the Riello iPlug/iDialog's load figures.
- Any comparison against a numeric NUT value must parse to float first. The
  textual form is not stable across drivers, NUT versions and client
  implementations; string matching silently failed against real hardware once
  while passing fixtures copied from `upsc`.
- `formatFloat` uses `strconv.FormatFloat(v, 'f', -1, 64)` — no trailing zeros.

## NUT protocol client

`internal/nut/client.go` speaks the upsd protocol directly; there is no third
party NUT library. It replaced `github.com/robbiet480/go.nut`, which has no way
to close a socket — no `Close` method, an unexported connection field, and a
`Disconnect` that only sends `LOGOUT`. Since `Poll` re-dials when a connection
goes stale, every failed poll leaked one socket for as long as the UPS was
unreachable.

- A poll is one round trip: `LIST VAR <ups>`. go.nut issued ~96, because
  `GetUPSList` built a full UPS object and fetched a description and type for
  every variable.
- Values are kept **verbatim** as upsd sends them, so `"50.10"` stays
  `"50.10"`. Only `VarsToMap` normalises, by trimming and dropping sentinels —
  both remove transport artefacts rather than changing what a value means.
  Do not "tidy" raw values: the old client coerced `"enabled"`/`"disabled"` to
  booleans and numeric-looking strings through ParseFloat, so
  `ups.beeper.status` published as `"true"`, `"false"` or `"muted"` depending
  on the reading — three type universes for one enum — and a version string
  like `"0.10"` would have become `"0.1"`.
- The split is deliberate: `state.variables` is what the device said,
  `computed` is what we calculated. Computed values are ours to define and
  **are** normalised, via `formatFloat`. Interpretation belongs in `computed`,
  where it is explicit and typed, never applied invisibly to raw data.
- Every network operation has a deadline (`Client.Timeout`, default
  `DefaultTimeout`), so nothing can block the poll loop indefinitely.
- `Poll` re-dials once on failure before giving up, so a upsd restart or a
  dropped connection heals on the next poll.
- Tests run against `fakeUPSD`, an in-process server speaking the real
  protocol, plus captured wire-format fixtures from every real device in
  `internal/nut/testdata/`. Add a fixture when a new device appears.
