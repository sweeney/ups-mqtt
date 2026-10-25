package publisher_test

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/sweeney/ups-mqtt/internal/metrics"
	"github.com/sweeney/ups-mqtt/internal/outages"
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

// decodeState unmarshals the published state payload.
func decodeState(t *testing.T, fp *publisher.FakePublisher) publisher.StateMessage {
	t.Helper()
	msg, ok := fp.Find("ups/cyberpower/state")
	if !ok {
		t.Fatal("state topic not published")
	}
	var state publisher.StateMessage
	if err := json.Unmarshal([]byte(msg.Payload), &state); err != nil {
		t.Fatalf("state payload invalid JSON: %v", err)
	}
	return state
}

// ---- Variable topic routing -----------------------------------------------

// Raw NUT variables are no longer published as individual topics; they
// duplicated what the state topic already carries, and no subscriber read them.
func TestPublishAll_NoRawVariableTopics(t *testing.T) {
	fp := runPublishAll(t)
	for _, topic := range []string{
		"ups/cyberpower/battery/charge",
		"ups/cyberpower/ups/load",
		"ups/cyberpower/ups/status",
		"ups/cyberpower/input/voltage",
	} {
		if _, ok := fp.Find(topic); ok {
			t.Errorf("raw variable topic %q should no longer be published", topic)
		}
	}
}

// Only computed/ scalars and the state topic go out.
func TestPublishAll_PublishesOnlyComputedAndState(t *testing.T) {
	fp := runPublishAll(t)
	for _, msg := range fp.Messages {
		rest := strings.TrimPrefix(msg.Topic, "ups/cyberpower/")
		if rest != "state" && !strings.HasPrefix(rest, "computed/") {
			t.Errorf("unexpected topic published: %q", msg.Topic)
		}
	}
	if _, ok := fp.Find("ups/cyberpower/state"); !ok {
		t.Error("state topic must still be published")
	}
}

// The values themselves must survive inside the state payload.
func TestPublishAll_VariablesCarriedInState(t *testing.T) {
	fp := runPublishAll(t)
	state := decodeState(t, fp)
	for name, want := range map[string]string{
		"battery.charge": "100",
		"ups.load":       "8",
		"ups.status":     "OL",
	} {
		if got := state.Variables[name]; got != want {
			t.Errorf("state.variables[%q] = %q, want %q", name, got, want)
		}
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
	if val(state.Computed.LoadWatts) != 72 {
		t.Errorf("computed.load_watts = %v, want 72", val(state.Computed.LoadWatts))
	}
	if state.Computed.StatusDisplay != "Online" {
		t.Errorf("computed.status_display = %q, want %q", state.Computed.StatusDisplay, "Online")
	}
	if state.Computed.OnBattery {
		t.Error("computed.on_battery should be false")
	}
}

// clock_synced on the state says whether its timestamp can be trusted; it is
// absent, not false, where sync cannot be determined.
func TestPublishAll_StateClockSynced(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		name   string
		synced *bool
		want   string // substring of the payload; "" means the key is absent
	}{
		{"synced", &yes, `"clock_synced":true`},
		{"unsynced", &no, `"clock_synced":false`},
		{"unknown", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fp := &publisher.FakePublisher{}
			cfg := publisher.PublishConfig{Prefix: "ups", UPSName: "cyberpower", ClockSynced: tc.synced}
			if err := publisher.PublishAll(sampleVars, metrics.Compute(sampleVars), cfg, fp); err != nil {
				t.Fatalf("PublishAll: %v", err)
			}
			msg, ok := fp.Find("ups/cyberpower/state")
			if !ok {
				t.Fatal("state topic not published")
			}
			if tc.want == "" {
				if strings.Contains(msg.Payload, "clock_synced") {
					t.Errorf("clock_synced present with sync unknown: %s", msg.Payload)
				}
			} else if !strings.Contains(msg.Payload, tc.want) {
				t.Errorf("payload lacks %s: %s", tc.want, msg.Payload)
			}
		})
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
	fp.Publish(publisher.Message{Topic: "a/b", Payload: "v1"}) //nolint:errcheck
	fp.Publish(publisher.Message{Topic: "c/d", Payload: "v2"}) //nolint:errcheck

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

func TestFakePublisher_Retained(t *testing.T) {
	fp := &publisher.FakePublisher{}
	if _, ok := fp.Retained("t"); ok {
		t.Fatal("nothing published: Retained should report false")
	}

	fp.Publish(publisher.Message{Topic: "t", Payload: "v1", Retained: true})    //nolint:errcheck
	fp.Publish(publisher.Message{Topic: "t", Payload: "v2", Retained: true})    //nolint:errcheck
	fp.Publish(publisher.Message{Topic: "t", Payload: "live"})                  //nolint:errcheck
	fp.Publish(publisher.Message{Topic: "u", Payload: "other", Retained: true}) //nolint:errcheck
	msg, ok := fp.Retained("t")
	if !ok || msg.Payload != "v2" {
		t.Errorf("Retained = %q, %v; want the last retained payload %q", msg.Payload, ok, "v2")
	}

	// An empty retained payload deletes the retained message.
	fp.Publish(publisher.Message{Topic: "t", Payload: "", Retained: true}) //nolint:errcheck
	if msg, ok := fp.Retained("t"); ok {
		t.Errorf("empty retained payload should delete the retained message, got %q", msg.Payload)
	}
	if _, ok := fp.Retained("u"); !ok {
		t.Error("deleting one topic must not affect another")
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
	if err := publisher.PublishOutage(onBatteryVars, m, outageStart, nil, cfg, fp); err != nil {
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
	if val(out.BatteryChargePct) != 95 {
		t.Errorf("battery_charge_pct = %v, want 95", val(out.BatteryChargePct))
	}
	if val(out.BatteryRuntimeSecs) != 4090 {
		t.Errorf("battery_runtime_secs = %v, want 4090", val(out.BatteryRuntimeSecs))
	}
	if val(out.BatteryRuntimeMins) != 68.17 {
		t.Errorf("battery_runtime_mins = %v, want 68.17", val(out.BatteryRuntimeMins))
	}
}

func TestPublishOutage_LoadWatts(t *testing.T) {
	_, out := runPublishOutage(t, time.Now().Add(-30*time.Second))
	// 8% × 900W = 72W
	if val(out.LoadWatts) != 72 {
		t.Errorf("load_watts = %v, want 72", val(out.LoadWatts))
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

// val dereferences an optional metric for comparison in assertions.
// A nil pointer means the UPS could not supply the value; tests that care
// about absence assert on the pointer itself instead.
func val(p *float64) float64 {
	if p == nil {
		return math.NaN()
	}
	return *p
}

// ── Riello iPlug: a UPS that reports no battery runtime or charge ───────────

// rielloOnBatteryVars is a real poll of a Riello iPlug taken during a mains
// cut.  It reports no battery.runtime and no battery.charge — those variables
// simply do not exist on this device.
var rielloOnBatteryVars = map[string]string{
	"ups.status":             "OB",
	"ups.load":               "0",
	"ups.realpower.nominal":  "480",
	"input.voltage":          "4",
	"input.frequency":        "0.00",
	"output.voltage":         "230",
	"output.voltage.nominal": "230",
	"battery.voltage":        "13.1",
}

func runRielloOutage(t *testing.T) (publisher.OutageMessage, string) {
	t.Helper()
	m := metrics.Compute(rielloOnBatteryVars)
	fp := &publisher.FakePublisher{}
	cfg := publisher.PublishConfig{Prefix: "ups", UPSName: "iplug", Retained: true}
	if err := publisher.PublishOutage(rielloOnBatteryVars, m, time.Now().Add(-30*time.Second), nil, cfg, fp); err != nil {
		t.Fatalf("PublishOutage: %v", err)
	}
	msg, ok := fp.Find("ups/iplug/outage")
	if !ok {
		t.Fatal("outage topic not published")
	}
	var out publisher.OutageMessage
	if err := json.Unmarshal([]byte(msg.Payload), &out); err != nil {
		t.Fatalf("outage payload invalid JSON: %v\npayload: %s", err, msg.Payload)
	}
	return out, msg.Payload
}

// The critical regression: with no runtime reported, the old code dated
// estimated_depletion_at to the current instant, announcing mid-outage that
// the battery was about to die.  It must now be omitted entirely.
func TestPublishOutage_NoRuntime_OmitsDepletionEstimate(t *testing.T) {
	out, payload := runRielloOutage(t)

	if out.EstimatedDepletionAt != "" {
		t.Errorf("estimated_depletion_at = %q, want empty when runtime is unknown", out.EstimatedDepletionAt)
	}
	if strings.Contains(payload, "estimated_depletion_at") {
		t.Errorf("estimated_depletion_at should be absent from the JSON entirely:\n%s", payload)
	}
}

func TestPublishOutage_NoRuntime_OmitsBatteryFields(t *testing.T) {
	out, payload := runRielloOutage(t)

	if out.BatteryRuntimeSecs != nil {
		t.Errorf("battery_runtime_secs = %v, want nil", *out.BatteryRuntimeSecs)
	}
	if out.BatteryRuntimeMins != nil {
		t.Errorf("battery_runtime_mins = %v, want nil", *out.BatteryRuntimeMins)
	}
	if out.BatteryChargePct != nil {
		t.Errorf("battery_charge_pct = %v, want nil", *out.BatteryChargePct)
	}
	for _, key := range []string{"battery_runtime_secs", "battery_runtime_mins", "battery_charge_pct"} {
		if strings.Contains(payload, key) {
			t.Errorf("%s should be absent from the JSON:\n%s", key, payload)
		}
	}
}

// battery.voltage is the only battery signal this UPS gives, so it is carried
// through as a coarse substitute for a charge percentage.
func TestPublishOutage_CarriesBatteryVoltage(t *testing.T) {
	out, _ := runRielloOutage(t)
	if out.BatteryVoltage == nil {
		t.Fatal("battery_voltage = nil, want 13.1")
	}
	if *out.BatteryVoltage != 13.1 {
		t.Errorf("battery_voltage = %v, want 13.1", *out.BatteryVoltage)
	}
}

// Facts the UPS does report must still be present.
func TestPublishOutage_NoRuntime_KeepsKnownFields(t *testing.T) {
	out, _ := runRielloOutage(t)
	if out.Status != "OB" {
		t.Errorf("status = %q, want OB", out.Status)
	}
	if out.StatusDisplay != "On Battery" {
		t.Errorf("status_display = %q, want %q", out.StatusDisplay, "On Battery")
	}
	if val(out.LoadWatts) != 0 {
		t.Errorf("load_watts = %v, want 0 (a real zero-load reading)", val(out.LoadWatts))
	}
	if out.OutageDurationSecs < 29 || out.OutageDurationSecs > 31 {
		t.Errorf("outage_duration_secs = %d, want ~30", out.OutageDurationSecs)
	}
}

// ── PublishAll must attempt every message, not stop at the first failure ───

// publishAllVars is a small, fixed variable set so message counts are exact.
var publishAllVars = map[string]string{
	"ups.status":            "OL",
	"ups.load":              "8",
	"ups.realpower.nominal": "900",
	"battery.runtime":       "4920",
	"input.voltage":         "242.0",
	"input.voltage.nominal": "230",
}

func publishAllCfg() publisher.PublishConfig {
	return publisher.PublishConfig{Prefix: "ups", UPSName: "cyberpower", Retained: true}
}

// A poll publishes 7 computed topics plus the state topic: 8 messages.
const wantMessagesPerPoll = 8

// A failure partway through must not abandon the remaining topics.  Go
// randomises map iteration order, so without this the set of lost topics would
// differ from poll to poll.
func TestPublishAll_ContinuesAfterFailure(t *testing.T) {
	failing := "ups/cyberpower/computed/load_watts"
	fp := &publisher.FakePublisher{FailTopic: func(tp string) bool { return tp == failing }}
	m := metrics.Compute(publishAllVars)

	err := publisher.PublishAll(publishAllVars, m, publishAllCfg(), fp)
	if err == nil {
		t.Fatal("expected the failed topic to be reported")
	}
	if len(fp.Messages) != wantMessagesPerPoll-1 {
		t.Errorf("published %d messages, want %d (everything except the one failure)",
			len(fp.Messages), wantMessagesPerPoll-1)
	}
	if len(fp.Failed) != 1 || fp.Failed[0] != failing {
		t.Errorf("Failed = %v, want exactly [%s]", fp.Failed, failing)
	}
}

// The state topic is published last and is also the LWT topic, so it is the
// message most at risk from an early return.
func TestPublishAll_StatePublishedDespiteEarlierFailure(t *testing.T) {
	fp := &publisher.FakePublisher{
		FailTopic: func(tp string) bool { return strings.HasPrefix(tp, "ups/cyberpower/computed/") },
	}
	m := metrics.Compute(publishAllVars)

	if err := publisher.PublishAll(publishAllVars, m, publishAllCfg(), fp); err == nil {
		t.Fatal("expected an error")
	}
	if _, ok := fp.Find("ups/cyberpower/state"); !ok {
		t.Error("state topic must still be published after every computed topic failed")
	}
}

// One failing computed topic must not take the others with it.
func TestPublishAll_ComputedSurviveOneFailure(t *testing.T) {
	fp := &publisher.FakePublisher{
		FailTopic: func(tp string) bool { return tp == "ups/cyberpower/computed/status_display" },
	}
	m := metrics.Compute(publishAllVars)

	if err := publisher.PublishAll(publishAllVars, m, publishAllCfg(), fp); err == nil {
		t.Fatal("expected an error")
	}
	for _, topic := range []string{
		"ups/cyberpower/computed/load_watts",
		"ups/cyberpower/computed/on_battery",
		"ups/cyberpower/computed/battery_runtime_mins",
	} {
		if _, ok := fp.Find(topic); !ok {
			t.Errorf("computed topic %q was dropped after a sibling failed", topic)
		}
	}
}

// Every failure must be reported, not just the first.
func TestPublishAll_AllFailuresReported(t *testing.T) {
	fp := &publisher.FakePublisher{FailTopic: func(string) bool { return true }}
	m := metrics.Compute(publishAllVars)

	err := publisher.PublishAll(publishAllVars, m, publishAllCfg(), fp)
	if err == nil {
		t.Fatal("expected an error when every publish fails")
	}
	if len(fp.Failed) != wantMessagesPerPoll {
		t.Errorf("attempted %d publishes, want %d — some were skipped",
			len(fp.Failed), wantMessagesPerPoll)
	}
	// errors.Join separates with newlines; every failure should appear.
	if got := strings.Count(err.Error(), "\n") + 1; got != wantMessagesPerPoll {
		t.Errorf("error reports %d failures, want %d: %v", got, wantMessagesPerPoll, err)
	}
}

func TestPublishAll_NoErrorWhenAllSucceed(t *testing.T) {
	fp := &publisher.FakePublisher{}
	m := metrics.Compute(publishAllVars)

	if err := publisher.PublishAll(publishAllVars, m, publishAllCfg(), fp); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fp.Messages) != wantMessagesPerPoll {
		t.Errorf("published %d messages, want %d", len(fp.Messages), wantMessagesPerPoll)
	}
	if len(fp.Failed) != 0 {
		t.Errorf("Failed = %v, want empty", fp.Failed)
	}
}

// ── state.variables denylist ──────────────────────────────────────────────

// noisyVars mirrors a real Riello poll, including the families we exclude.
var noisyVars = map[string]string{
	"ups.status":                  "OL",
	"input.voltage":               "247",
	"battery.charge":              "100",
	"battery.voltage":             "13.5",
	"output.L1.current":           "0",
	"output.L2.current":           "0",
	"output.L2.power":             "0",
	"output.L2.realpower":         "0",
	"output.L3.current":           "0",
	"output.L3.power":             "0",
	"output.L3.realpower":         "0",
	"driver.name":                 "riello_usb",
	"driver.state":                "quiet",
	"driver.version":              "2.8.1",
	"driver.parameter.vendorid":   "04B4",
	"driver.version.usb":          "libusb-1.0.28",
	"driver.flag.allow_killpower": "0",
}

func stateFrom(t *testing.T, vars map[string]string) publisher.StateMessage {
	t.Helper()
	fp := &publisher.FakePublisher{}
	m := metrics.Compute(vars)
	if err := publisher.PublishAll(vars, m, publishAllCfg(), fp); err != nil {
		t.Fatalf("PublishAll: %v", err)
	}
	return decodeState(t, fp)
}

// driver.* is NUT's own metadata, not UPS telemetry, and driver.state is the
// noisiest value in the system.
func TestState_ExcludesDriverVariables(t *testing.T) {
	state := stateFrom(t, noisyVars)
	for name := range state.Variables {
		if strings.HasPrefix(name, "driver.") {
			t.Errorf("state.variables contains %q; driver.* must be excluded", name)
		}
	}
}

// L2/L3 are phantom phases on a single-phase UPS, permanently zero.
func TestState_ExcludesPhantomPhases(t *testing.T) {
	state := stateFrom(t, noisyVars)
	for name := range state.Variables {
		if strings.HasPrefix(name, "output.L2.") || strings.HasPrefix(name, "output.L3.") {
			t.Errorf("state.variables contains %q; phantom phases must be excluded", name)
		}
	}
	// The real phase must survive.
	if _, ok := state.Variables["output.L1.current"]; !ok {
		t.Error("output.L1.current was excluded; only L2/L3 are phantom")
	}
}

// The statehouse adapter reads these two straight out of state.variables, so
// they are a live contract and must never be filtered.
func TestState_KeepsAdapterContractVariables(t *testing.T) {
	state := stateFrom(t, noisyVars)
	for _, name := range []string{"battery.charge", "input.voltage"} {
		if _, ok := state.Variables[name]; !ok {
			t.Errorf("state.variables is missing %q, which the statehouse adapter reads directly", name)
		}
	}
}

func TestState_KeepsEverythingElse(t *testing.T) {
	state := stateFrom(t, noisyVars)
	for _, name := range []string{"ups.status", "battery.voltage", "output.L1.current"} {
		if _, ok := state.Variables[name]; !ok {
			t.Errorf("state.variables is missing %q", name)
		}
	}
	// 17 inputs - 6 driver.* - 6 phantom phases = 5.
	if len(state.Variables) != 5 {
		t.Errorf("state.variables has %d entries, want 5: %v", len(state.Variables), state.Variables)
	}
}

// The denylist matches whole families by prefix, and must not catch variables
// that merely look similar.
func TestState_DenylistDoesNotOverreach(t *testing.T) {
	state := stateFrom(t, map[string]string{
		"ups.status":        "OL",
		"driverless.thing":  "keep",
		"output.L1.current": "0",
		"output.L10.power":  "keep",
	})
	for _, name := range []string{"driverless.thing", "output.L10.power", "output.L1.current"} {
		if _, ok := state.Variables[name]; !ok {
			t.Errorf("state.variables is missing %q; the denylist over-matched", name)
		}
	}
}

// Filtering must not mutate the caller's map — metrics are computed from it.
func TestState_DoesNotMutateInputVariables(t *testing.T) {
	vars := map[string]string{"ups.status": "OL", "driver.state": "quiet"}
	before := len(vars)
	stateFrom(t, vars)
	if len(vars) != before {
		t.Errorf("input map now has %d entries, was %d — PublishAll mutated its argument", len(vars), before)
	}
	if _, ok := vars["driver.state"]; !ok {
		t.Error("driver.state was removed from the caller's map")
	}
}

// ── clock_synced on the outage message ────────────────────────────────────

func TestPublishOutage_ClockSynced(t *testing.T) {
	m := metrics.Compute(onBatteryVars)
	cfg := publisher.PublishConfig{Prefix: "ups", UPSName: "cyberpower"}
	for _, tc := range []struct {
		synced *bool
		want   string
	}{
		{boolPtr(true), `"clock_synced":true`},
		{boolPtr(false), `"clock_synced":false`},
	} {
		fp := &publisher.FakePublisher{}
		if err := publisher.PublishOutage(onBatteryVars, m, time.Now(), tc.synced, cfg, fp); err != nil {
			t.Fatal(err)
		}
		if msg, _ := fp.Find("ups/cyberpower/outage"); !strings.Contains(msg.Payload, tc.want) {
			t.Errorf("payload %s does not contain %s", msg.Payload, tc.want)
		}
	}
	// Unknown is absent, not false.
	fp := &publisher.FakePublisher{}
	if err := publisher.PublishOutage(onBatteryVars, m, time.Now(), nil, cfg, fp); err != nil {
		t.Fatal(err)
	}
	if msg, _ := fp.Find("ups/cyberpower/outage"); strings.Contains(msg.Payload, "clock_synced") {
		t.Errorf("unknown clock sync published: %s", msg.Payload)
	}
}

func boolPtr(b bool) *bool { return &b }

// ── last_outage ────────────────────────────────────────────────────────────

func TestPublishLastOutage(t *testing.T) {
	start := time.Date(2026, 10, 13, 10, 2, 10, 0, time.UTC)
	s := outages.Summary{
		StartedAt:           start,
		EndedAt:             start.Add(2270 * time.Second),
		LowBatteryReached:   true,
		MinBatteryChargePct: func() *float64 { v := 61.0; return &v }(),
		ClockSynced:         boolPtr(true),
		SpansRestart:        false,
		Final:               true,
	}
	fp := &publisher.FakePublisher{}
	cfg := publisher.PublishConfig{Prefix: "ups", UPSName: "site-ups", TelemetryQOS: 0, EventQOS: 1}
	if err := publisher.PublishLastOutage(s, cfg, fp); err != nil {
		t.Fatal(err)
	}
	msg, ok := fp.Retained("ups/site-ups/last_outage")
	if !ok {
		t.Fatal("last_outage not retained")
	}
	if msg.QOS != 1 {
		t.Errorf("QoS = %d, want the event QoS 1", msg.QOS)
	}
	want := `{"ups_name":"site-ups","started_at":"2026-10-13T10:02:10Z","ended_at":"2026-10-13T10:40:00Z",` +
		`"duration_secs":2270,"low_battery_reached":true,"min_battery_charge_pct":61,"clock_synced":true,"spans_restart":false}`
	if msg.Payload != want {
		t.Errorf("payload\n got %s\nwant %s", msg.Payload, want)
	}
}

// Unknowns are absent, not zero: no charge reading (the Riello iPlug, or
// across a restart) and no way to tell clock sync.
func TestPublishLastOutage_UnknownsAbsent(t *testing.T) {
	start := time.Now()
	fp := &publisher.FakePublisher{}
	s := outages.Summary{StartedAt: start, EndedAt: start.Add(time.Minute), SpansRestart: true}
	if err := publisher.PublishLastOutage(s, publisher.PublishConfig{Prefix: "ups", UPSName: "x"}, fp); err != nil {
		t.Fatal(err)
	}
	msg, _ := fp.Find("ups/x/last_outage")
	for _, absent := range []string{"min_battery_charge_pct", "clock_synced"} {
		if strings.Contains(msg.Payload, absent) {
			t.Errorf("payload has %s: %s", absent, msg.Payload)
		}
	}
	if !strings.Contains(msg.Payload, `"spans_restart":true`) {
		t.Errorf("payload lacks spans_restart: %s", msg.Payload)
	}
}

// A charge or runtime that cannot be real is unknown: no percentage, no
// runtime and no depletion date 38 days out.
func TestPublishOutage_ImpossibleBatteryFieldsOmitted(t *testing.T) {
	vars := map[string]string{
		"ups.status":      "OB",
		"battery.charge":  "3434",
		"battery.runtime": "3308760",
		"battery.voltage": "13.1",
	}
	fp := &publisher.FakePublisher{}
	cfg := publisher.PublishConfig{Prefix: "ups", UPSName: "iplug", Retained: true}
	if err := publisher.PublishOutage(vars, metrics.Compute(vars), time.Now().Add(-30*time.Second), nil, cfg, fp); err != nil {
		t.Fatalf("PublishOutage: %v", err)
	}
	msg, ok := fp.Find("ups/iplug/outage")
	if !ok {
		t.Fatal("outage topic not published")
	}
	for _, key := range []string{"battery_charge_pct", "battery_runtime_secs", "battery_runtime_mins", "estimated_depletion_at"} {
		if strings.Contains(msg.Payload, key) {
			t.Errorf("%s should be absent:\n%s", key, msg.Payload)
		}
	}
	if !strings.Contains(msg.Payload, `"battery_voltage":13.1`) {
		t.Errorf("battery_voltage lost:\n%s", msg.Payload)
	}
}
