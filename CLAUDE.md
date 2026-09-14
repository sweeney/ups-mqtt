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
| `github.com/robbiet480/go.nut` | NUT TCP client — `Connect(host, port)` returns `Client` (value type); `GetUPSList()` returns `[]UPS` |
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
  one event this daemon exists to report.
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
- Any comparison against a numeric NUT value must parse to float first. go.nut
  decodes values into `interface{}` and renders them with `%v`, so upsd's
  `"409.50"` reaches the code as `"409.5"`; string matching silently fails
  against real hardware while passing fixtures copied from `upsc`.
- `formatFloat` uses `strconv.FormatFloat(v, 'f', -1, 64)` — no trailing zeros.

## go.nut API notes

- `gonut.Connect(host string, port ...int) (Client, error)` — port is variadic int
- `client.GetUPSList() ([]UPS, error)` — iterate to find by `UPS.Name`
- `UPS.GetVariables() ([]Variable, error)` — `Variable.Value` is `interface{}`; use `fmt.Sprintf("%v", v.Value)`
- `client.Disconnect() (bool, error)`
