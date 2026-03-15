package publisher_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

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

// ---- OutageTopic ----------------------------------------------------------

func TestOutageTopic(t *testing.T) {
	got := publisher.OutageTopic("home", "myups")
	if got != "home/myups/outage" {
		t.Errorf("OutageTopic = %q, want %q", got, "home/myups/outage")
	}
}

// ---- PublishOutage --------------------------------------------------------

var onBatteryVars = map[string]string{
	"ups.status":            "OB DISCHRG",
	"ups.load":              "8",
	"ups.realpower.nominal": "900",
	"battery.charge":        "95",
	"battery.runtime":       "4090",
}

func runPublishOutage(t *testing.T, outageStart time.Time) (*publisher.FakePublisher, publisher.OutageMessage) {
	t.Helper()
	m := metrics.Compute(onBatteryVars)
	fp := &publisher.FakePublisher{}
	cfg := publisher.PublishConfig{Prefix: "ups", UPSName: "cyberpower", Retained: true}
	if err := publisher.PublishOutage(onBatteryVars, m, outageStart, cfg, fp); err != nil {
		t.Fatalf("PublishOutage: %v", err)
	}
	msg, ok := fp.Find("ups/cyberpower/outage")
	if !ok {
		t.Fatal("outage topic not published")
	}
	var out publisher.OutageMessage
	if err := json.Unmarshal([]byte(msg.Payload), &out); err != nil {
		t.Fatalf("outage payload invalid JSON: %v\npayload: %s", err, msg.Payload)
	}
	return fp, out
}

func TestPublishOutage_TopicAndRetained(t *testing.T) {
	fp, _ := runPublishOutage(t, time.Now().Add(-30*time.Second))
	msg, _ := fp.Find("ups/cyberpower/outage")
	if !msg.Retained {
		t.Error("outage message should always be retained")
	}
}

func TestPublishOutage_UPSName(t *testing.T) {
	_, out := runPublishOutage(t, time.Now().Add(-30*time.Second))
	if out.UPSName != "cyberpower" {
		t.Errorf("ups_name = %q, want %q", out.UPSName, "cyberpower")
	}
}

func TestPublishOutage_Status(t *testing.T) {
	_, out := runPublishOutage(t, time.Now().Add(-30*time.Second))
	if out.Status != "OB DISCHRG" {
		t.Errorf("status = %q, want %q", out.Status, "OB DISCHRG")
	}
	if out.StatusDisplay != "On Battery, Discharging" {
		t.Errorf("status_display = %q, want %q", out.StatusDisplay, "On Battery, Discharging")
	}
}

func TestPublishOutage_BatteryFields(t *testing.T) {
	_, out := runPublishOutage(t, time.Now().Add(-30*time.Second))
	if out.BatteryChargePct != 95 {
		t.Errorf("battery_charge_pct = %v, want 95", out.BatteryChargePct)
	}
	if out.BatteryRuntimeSecs != 4090 {
		t.Errorf("battery_runtime_secs = %v, want 4090", out.BatteryRuntimeSecs)
	}
	if out.BatteryRuntimeMins != 68.17 {
		t.Errorf("battery_runtime_mins = %v, want 68.17", out.BatteryRuntimeMins)
	}
}

func TestPublishOutage_LoadWatts(t *testing.T) {
	_, out := runPublishOutage(t, time.Now().Add(-30*time.Second))
	// 8% × 900W = 72W
	if out.LoadWatts != 72 {
		t.Errorf("load_watts = %v, want 72", out.LoadWatts)
	}
}

func TestPublishOutage_OutageDuration(t *testing.T) {
	start := time.Now().Add(-90 * time.Second)
	_, out := runPublishOutage(t, start)
	if out.OutageDurationSecs < 89 || out.OutageDurationSecs > 95 {
		t.Errorf("outage_duration_secs = %d, want ~90", out.OutageDurationSecs)
	}
	if out.OutageStartedAt == "" {
		t.Error("outage_started_at should not be empty")
	}
}

func TestPublishOutage_Timestamps(t *testing.T) {
	_, out := runPublishOutage(t, time.Now().Add(-30*time.Second))
	if _, err := time.Parse(time.RFC3339, out.Timestamp); err != nil {
		t.Errorf("timestamp %q is not RFC3339: %v", out.Timestamp, err)
	}
	if _, err := time.Parse(time.RFC3339, out.OutageStartedAt); err != nil {
		t.Errorf("outage_started_at %q is not RFC3339: %v", out.OutageStartedAt, err)
	}
	if _, err := time.Parse(time.RFC3339, out.EstimatedDepletionAt); err != nil {
		t.Errorf("estimated_depletion_at %q is not RFC3339: %v", out.EstimatedDepletionAt, err)
	}
}

// ---- ClearOutage ----------------------------------------------------------

func TestClearOutage_EmptyRetainedPayload(t *testing.T) {
	fp := &publisher.FakePublisher{}
	cfg := publisher.PublishConfig{Prefix: "ups", UPSName: "cyberpower"}
	if err := publisher.ClearOutage(cfg, fp); err != nil {
		t.Fatalf("ClearOutage: %v", err)
	}
	msg, ok := fp.Find("ups/cyberpower/outage")
	if !ok {
		t.Fatal("clear message not published")
	}
	if msg.Payload != "" {
		t.Errorf("clear payload = %q, want empty", msg.Payload)
	}
	if !msg.Retained {
		t.Error("clear message must be retained to erase the broker's retained copy")
	}
}

// ---- TestPublishAll_VarsPublishError verifies the error path when a variable
// topic publish fails (non-empty vars map so the vars loop is entered).
func TestPublishAll_VarsPublishError(t *testing.T) {
	fp := &publisher.FakePublisher{PublishError: errors.New("broker down")}
	m := metrics.Compute(sampleVars)
	cfg := publisher.PublishConfig{Prefix: "ups", UPSName: "test", Retained: false}
	err := publisher.PublishAll(sampleVars, m, cfg, fp)
	if err == nil {
		t.Fatal("expected error when vars publish fails")
	}
}
