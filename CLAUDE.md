# ups-mqtt

Go daemon that polls a NUT (Network UPS Tools) server and publishes UPS state to MQTT.
Device: CyberPower CP1500EPFCLCD (900W).  Runs as a systemd service.

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
internal/config/config.go      Config + TOML loader + env overrides
internal/nut/                  Poller interface, real client, FakePoller
internal/metrics/              pure computed metrics (100% test coverage)
internal/publisher/            Publisher interface, topic routing, JSON, FakePublisher
internal/integration_test.go   end-to-end: FakePoller → metrics → FakePublisher
```

## Test / build commands

```bash
go test -race ./...                             # all tests
go test -coverprofile=c.out ./internal/metrics/ # must be 100%
go vet ./...                                    # must be silent
go build ./...                                  # clean compile
```

## Conventions

- `internal/metrics` coverage must remain **100%** — CI enforces this.
- `FakePoller` and `FakePublisher` follow boiler-sensor conventions: exported
  fields (`Variables`, `Err`, `CallCount`, `Closed`, `Messages`, `PublishError`,
  plus `FailTopic`/`Failed` on FakePublisher for simulating a partially degraded
  link), a `Reset()` method, and `Find(topic)` / `IndexOf(topic)` helpers on
  FakePublisher — `IndexOf` is how publish ordering is asserted.
- The middle topic segment is the **label** (`NUTConfig.EffectiveLabel()`), which
  falls back to `ups_name`. Use it for topic routing; `ups_name` is only for the
  NUT device lookup.
- Only two things are published per poll: computed metrics under
  `{prefix}/{label}/computed/`, and the combined state JSON on
  `{prefix}/{label}/state`. Raw NUT variables are **not** published as
  individual topics — they duplicated the state payload and nothing read them.
  They travel inside `state.variables`.
- `{prefix}/{label}/outage` is published while on battery and cleared with an
  empty retained payload on recovery. It goes out **before** the telemetry on
  every poll and is never suppressed by a telemetry failure; a power cut is the
  one event this daemon exists to report. A fresh process does not know
  whether the topic is clear (a predecessor may have died mid-outage, e.g. the
  host lost power), so the first poll on mains clears it unconditionally.
  `TestRestartAfterOutage_*` replays that against the Riello fixture with
  `FakePublisher.Retained` standing in for the broker.
- QoS is per message, not per client: telemetry (`state`, `computed/*`) goes
  at `mqtt.telemetry_qos`; events (`outage` and its clear, the offline
  announcement, the LWT) at `mqtt.event_qos`. Both fall back to `mqtt.qos`.
  Behind a bridged local broker, QoS 1 telemetry would fill the bridge's
  capped queue and get the outage dropped. A new publish must pick its kind.
- Startup retries the MQTT connect, then the NUT connect, via `retryConnect`
  (1 s doubling to 60 s, interruptible by signal). `NewMQTTPublisher` does not
  dial; it fails only on config errors, which are fatal.
- `state` is published every poll regardless of change, so it doubles as a
  heartbeat. It is also the LWT topic, so it carries two shapes: a full snapshot
  while alive, `{"online":false,…}` when the broker declares the client dead.
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
