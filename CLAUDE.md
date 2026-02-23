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
  fields (`Variables`, `Err`, `CallCount`, `Closed`, `Messages`, `PublishError`),
  plus a `Reset()` method and a `Find(topic)` helper on FakePublisher.
- NUT variable → MQTT topic: dots become slashes, prefixed with `{prefix}/{ups_name}/`.
- Computed metrics live under `{prefix}/{ups_name}/computed/`.
- Combined state JSON on `{prefix}/{ups_name}/state`.
- `formatFloat` uses `strconv.FormatFloat(v, 'f', -1, 64)` — no trailing zeros.

## go.nut API notes

- `gonut.Connect(host string, port ...int) (Client, error)` — port is variadic int
- `client.GetUPSList() ([]UPS, error)` — iterate to find by `UPS.Name`
- `UPS.GetVariables() ([]Variable, error)` — `Variable.Value` is `interface{}`; use `fmt.Sprintf("%v", v.Value)`
- `client.Disconnect() (bool, error)`
