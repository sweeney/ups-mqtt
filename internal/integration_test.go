// Package integration_test exercises the full pipeline:
//
//	FakePoller → metrics.Compute → publisher.PublishAll → FakePublisher
//
// No real NUT server or MQTT broker is needed.
package integration_test

import (
	"encoding/json"
	"testing"

	"github.com/sweeney/ups-mqtt/internal/metrics"
	"github.com/sweeney/ups-mqtt/internal/nut"
	"github.com/sweeney/ups-mqtt/internal/publisher"
)

// deviceVars are the variables from the sample CyberPower CP1500EPFCLCD
// (upsc.txt in the repo root).
var deviceVars = []nut.Variable{
	{Name: "battery.charge", Value: "100"},
	{Name: "battery.charge.low", Value: "10"},
	{Name: "battery.charge.warning", Value: "20"},
	{Name: "battery.runtime", Value: "4920"},
	{Name: "battery.runtime.low", Value: "300"},
	{Name: "battery.type", Value: "PbAcid"},
	{Name: "battery.voltage", Value: "24.0"},
	{Name: "battery.voltage.nominal", Value: "24"},
	{Name: "input.transfer.high", Value: "260"},
	{Name: "input.transfer.low", Value: "170"},
	{Name: "input.voltage", Value: "242.0"},
	{Name: "input.voltage.nominal", Value: "230"},
	{Name: "output.voltage", Value: "242.0"},
	{Name: "ups.beeper.status", Value: "disabled"},
	{Name: "ups.load", Value: "8"},
	{Name: "ups.model", Value: "CP1500EPFCLCD"},
	{Name: "ups.realpower.nominal", Value: "900"},
	{Name: "ups.status", Value: "OL"},
}

// runOnce is a thin wrapper over pollOnce (defined in integration_scenarios_test.go)
// for tests that only need the publisher, not the metrics.
func runOnce(t *testing.T, vars []nut.Variable) *publisher.FakePublisher {
	t.Helper()
	_, fpub := pollOnce(t, vars)
	return fpub
}

func TestEndToEnd_LoadWatts(t *testing.T) {
	fpub := runOnce(t, deviceVars)
	msg, ok := fpub.Find("ups/cyberpower/computed/load_watts")
	if !ok {
		t.Fatal("computed/load_watts not published")
	}
	// 8% × 900 W = 72 W
	if msg.Payload != "72" {
		t.Errorf("load_watts = %q, want %q", msg.Payload, "72")
	}
}

func TestEndToEnd_BatteryRuntimeMins(t *testing.T) {
	fpub := runOnce(t, deviceVars)
	msg, ok := fpub.Find("ups/cyberpower/computed/battery_runtime_mins")
	if !ok {
		t.Fatal("computed/battery_runtime_mins not published")
	}
	// 4920 s / 60 = 82 min
	if msg.Payload != "82" {
		t.Errorf("battery_runtime_mins = %q, want %q", msg.Payload, "82")
	}
}

func TestEndToEnd_BatteryRuntimeHours(t *testing.T) {
	fpub := runOnce(t, deviceVars)
	msg, ok := fpub.Find("ups/cyberpower/computed/battery_runtime_hours")
	if !ok {
		t.Fatal("computed/battery_runtime_hours not published")
	}
	// 4920 / 3600 ≈ 1.37
	if msg.Payload != "1.37" {
		t.Errorf("battery_runtime_hours = %q, want %q", msg.Payload, "1.37")
	}
}

func TestEndToEnd_InputVoltageDeviationPct(t *testing.T) {
	fpub := runOnce(t, deviceVars)
	msg, ok := fpub.Find("ups/cyberpower/computed/input_voltage_deviation_pct")
	if !ok {
		t.Fatal("computed/input_voltage_deviation_pct not published")
	}
	// (242 − 230) / 230 × 100 ≈ 5.22 %
	if msg.Payload != "5.22" {
		t.Errorf("input_voltage_deviation_pct = %q, want %q", msg.Payload, "5.22")
	}
}

func TestEndToEnd_StatusDisplay(t *testing.T) {
	fpub := runOnce(t, deviceVars)
	msg, ok := fpub.Find("ups/cyberpower/computed/status_display")
	if !ok {
		t.Fatal("computed/status_display not published")
	}
	if msg.Payload != "Online" {
		t.Errorf("status_display = %q, want %q", msg.Payload, "Online")
	}
}

func TestEndToEnd_VariableTopic(t *testing.T) {
	fpub := runOnce(t, deviceVars)
	msg, ok := fpub.Find("ups/cyberpower/battery/charge")
	if !ok {
		t.Fatal("battery/charge topic not published")
	}
	if msg.Payload != "100" {
		t.Errorf("battery/charge = %q, want %q", msg.Payload, "100")
	}
}

func TestEndToEnd_StateTopicJSON(t *testing.T) {
	fpub := runOnce(t, deviceVars)
	msg, ok := fpub.Find("ups/cyberpower/state")
	if !ok {
		t.Fatal("state topic not published")
	}

	var state publisher.StateMessage
	if err := json.Unmarshal([]byte(msg.Payload), &state); err != nil {
		t.Fatalf("state JSON invalid: %v\npayload: %s", err, msg.Payload)
	}
	if state.UPSName != "cyberpower" {
		t.Errorf("state.ups_name = %q, want %q", state.UPSName, "cyberpower")
	}
	if state.Computed.LoadWatts != 72 {
		t.Errorf("state.computed.load_watts = %v, want 72", state.Computed.LoadWatts)
	}
}

func TestEndToEnd_FakePollerCallCount(t *testing.T) {
	fp := &nut.FakePoller{Variables: deviceVars}
	fpub := &publisher.FakePublisher{}
	cfg := publisher.PublishConfig{Prefix: "ups", UPSName: "cyberpower", Retained: true}

	for i := 0; i < 3; i++ {
		vars, err := fp.Poll()
		if err != nil {
			t.Fatalf("Poll %d: %v", i, err)
		}
		m := metrics.Compute(nut.VarsToMap(vars))
		if err := publisher.PublishAll(nut.VarsToMap(vars), m, cfg, fpub); err != nil {
			t.Fatalf("PublishAll %d: %v", i, err)
		}
		fpub.Reset()
	}

	if fp.CallCount != 3 {
		t.Errorf("CallCount = %d after 3 polls, want 3", fp.CallCount)
	}
}

func TestEndToEnd_AllMessagesRetained(t *testing.T) {
	fpub := runOnce(t, deviceVars)
	for _, msg := range fpub.Messages {
		if !msg.Retained {
			t.Errorf("message on topic %q should be retained", msg.Topic)
		}
	}
}
