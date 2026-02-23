package publisher_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sweeney/ups-mqtt/internal/metrics"
	"github.com/sweeney/ups-mqtt/internal/publisher"
)

// sampleVars mirrors the actual device output from upsc.txt.
var sampleVars = map[string]string{
	"battery.charge":        "100",
	"ups.load":              "8",
	"ups.status":            "OL",
	"ups.realpower.nominal": "900",
	"battery.runtime":       "4920",
	"input.voltage":         "242.0",
	"input.voltage.nominal": "230",
}

func runPublishAll(t *testing.T) *publisher.FakePublisher {
	t.Helper()
	m := metrics.Compute(sampleVars)
	fp := &publisher.FakePublisher{}
	cfg := publisher.PublishConfig{Prefix: "ups", UPSName: "cyberpower", Retained: true}
	if err := publisher.PublishAll(sampleVars, m, cfg, fp); err != nil {
		t.Fatalf("PublishAll: %v", err)
	}
	return fp
}

// ---- Variable topic routing -----------------------------------------------

func TestPublishAll_VariableTopic_DotsToSlashes(t *testing.T) {
	fp := runPublishAll(t)
	msg, ok := fp.Find("ups/cyberpower/battery/charge")
	if !ok {
		t.Fatal("topic ups/cyberpower/battery/charge not published")
	}
	if msg.Payload != "100" {
		t.Errorf("payload = %q, want %q", msg.Payload, "100")
	}
	if !msg.Retained {
		t.Error("message should be retained")
	}
}

func TestPublishAll_VariableTopic_UpsLoad(t *testing.T) {
	fp := runPublishAll(t)
	msg, ok := fp.Find("ups/cyberpower/ups/load")
	if !ok {
		t.Fatal("topic ups/cyberpower/ups/load not published")
	}
	if msg.Payload != "8" {
		t.Errorf("payload = %q, want %q", msg.Payload, "8")
	}
}

func TestPublishAll_VariableTopic_UpsStatus(t *testing.T) {
	fp := runPublishAll(t)
	msg, ok := fp.Find("ups/cyberpower/ups/status")
	if !ok {
		t.Fatal("topic ups/cyberpower/ups/status not published")
	}
	if msg.Payload != "OL" {
		t.Errorf("payload = %q, want %q", msg.Payload, "OL")
	}
}

// ---- Computed metric topics -----------------------------------------------

func TestPublishAll_Computed_LoadWatts(t *testing.T) {
	fp := runPublishAll(t)
	msg, ok := fp.Find("ups/cyberpower/computed/load_watts")
	if !ok {
		t.Fatal("computed/load_watts not published")
	}
	if msg.Payload != "72" {
		t.Errorf("payload = %q, want %q", msg.Payload, "72")
	}
}

func TestPublishAll_Computed_BatteryRuntimeMins(t *testing.T) {
	fp := runPublishAll(t)
	msg, ok := fp.Find("ups/cyberpower/computed/battery_runtime_mins")
	if !ok {
		t.Fatal("computed/battery_runtime_mins not published")
	}
	if msg.Payload != "82" {
		t.Errorf("payload = %q, want %q", msg.Payload, "82")
	}
}

func TestPublishAll_Computed_BatteryRuntimeHours(t *testing.T) {
	fp := runPublishAll(t)
	msg, ok := fp.Find("ups/cyberpower/computed/battery_runtime_hours")
	if !ok {
		t.Fatal("computed/battery_runtime_hours not published")
	}
	if msg.Payload != "1.37" {
		t.Errorf("payload = %q, want %q", msg.Payload, "1.37")
	}
}

func TestPublishAll_Computed_OnBattery(t *testing.T) {
	fp := runPublishAll(t)
	msg, ok := fp.Find("ups/cyberpower/computed/on_battery")
	if !ok {
		t.Fatal("computed/on_battery not published")
	}
	if msg.Payload != "false" {
		t.Errorf("payload = %q, want %q", msg.Payload, "false")
	}
}

func TestPublishAll_Computed_LowBattery(t *testing.T) {
	fp := runPublishAll(t)
	msg, ok := fp.Find("ups/cyberpower/computed/low_battery")
	if !ok {
		t.Fatal("computed/low_battery not published")
	}
	if msg.Payload != "false" {
		t.Errorf("payload = %q, want %q", msg.Payload, "false")
	}
}

func TestPublishAll_Computed_StatusDisplay(t *testing.T) {
	fp := runPublishAll(t)
	msg, ok := fp.Find("ups/cyberpower/computed/status_display")
	if !ok {
		t.Fatal("computed/status_display not published")
	}
	if msg.Payload != "Online" {
		t.Errorf("payload = %q, want %q", msg.Payload, "Online")
	}
}

func TestPublishAll_Computed_InputVoltageDeviationPct(t *testing.T) {
	fp := runPublishAll(t)
	msg, ok := fp.Find("ups/cyberpower/computed/input_voltage_deviation_pct")
	if !ok {
		t.Fatal("computed/input_voltage_deviation_pct not published")
	}
	if msg.Payload != "5.22" {
		t.Errorf("payload = %q, want %q", msg.Payload, "5.22")
	}
}

// ---- JSON state topic -----------------------------------------------------

func TestPublishAll_StateTopic_Structure(t *testing.T) {
	fp := runPublishAll(t)
	msg, ok := fp.Find("ups/cyberpower/state")
	if !ok {
		t.Fatal("state topic not published")
	}

	var state publisher.StateMessage
	if err := json.Unmarshal([]byte(msg.Payload), &state); err != nil {
		t.Fatalf("state payload is not valid JSON: %v\npayload: %s", err, msg.Payload)
	}

	if state.UPSName != "cyberpower" {
		t.Errorf("ups_name = %q, want %q", state.UPSName, "cyberpower")
	}
	if state.Timestamp == "" {
		t.Error("timestamp should not be empty")
	}
	if state.Variables["battery.charge"] != "100" {
		t.Errorf("variables[battery.charge] = %q, want %q", state.Variables["battery.charge"], "100")
	}
	if state.Computed.LoadWatts != 72 {
		t.Errorf("computed.load_watts = %v, want 72", state.Computed.LoadWatts)
	}
	if state.Computed.StatusDisplay != "Online" {
		t.Errorf("computed.status_display = %q, want %q", state.Computed.StatusDisplay, "Online")
	}
	if state.Computed.OnBattery {
		t.Error("computed.on_battery should be false")
	}
}

// ---- StateTopic helper ----------------------------------------------------

func TestStateTopic(t *testing.T) {
	got := publisher.StateTopic("home", "myups")
	if got != "home/myups/state" {
		t.Errorf("StateTopic = %q, want %q", got, "home/myups/state")
	}
}

// ---- FormatOffline --------------------------------------------------------

func TestFormatOffline(t *testing.T) {
	payload := publisher.FormatOffline()
	if !strings.Contains(payload, `"online":false`) {
		t.Errorf("FormatOffline payload missing online:false: %s", payload)
	}
	if !strings.Contains(payload, `"timestamp"`) {
		t.Errorf("FormatOffline payload missing timestamp: %s", payload)
	}
}

// ---- FakePublisher --------------------------------------------------------

func TestFakePublisher_Find(t *testing.T) {
	fp := &publisher.FakePublisher{}
	fp.Publish(publisher.Message{Topic: "a/b", Payload: "v1"})  //nolint:errcheck
	fp.Publish(publisher.Message{Topic: "c/d", Payload: "v2"})  //nolint:errcheck

	msg, ok := fp.Find("c/d")
	if !ok {
		t.Fatal("Find should return true for existing topic")
	}
	if msg.Payload != "v2" {
		t.Errorf("Find payload = %q, want %q", msg.Payload, "v2")
	}

	_, ok = fp.Find("missing")
	if ok {
		t.Error("Find should return false for missing topic")
	}
}

func TestFakePublisher_PublishError(t *testing.T) {
	fp := &publisher.FakePublisher{PublishError: errors.New("broker down")}
	m := metrics.Compute(map[string]string{})
	cfg := publisher.PublishConfig{Prefix: "ups", UPSName: "test", Retained: false}
	err := publisher.PublishAll(map[string]string{}, m, cfg, fp)
	if err == nil {
		t.Fatal("expected error when PublishError is set")
	}
}

func TestFakePublisher_Close(t *testing.T) {
	fp := &publisher.FakePublisher{}
	if fp.Closed {
		t.Fatal("should not be closed initially")
	}
	fp.Close() //nolint:errcheck
	if !fp.Closed {
		t.Error("should be closed after Close()")
	}
}

func TestFakePublisher_Reset(t *testing.T) {
	fp := &publisher.FakePublisher{}
	fp.Publish(publisher.Message{Topic: "x", Payload: "y"}) //nolint:errcheck
	fp.Closed = true
	fp.Reset()

	if len(fp.Messages) != 0 {
		t.Error("Reset should clear Messages")
	}
	if fp.Closed {
		t.Error("Reset should set Closed=false")
	}
}
