package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sweeney/ups-mqtt/internal/config"
	"github.com/sweeney/ups-mqtt/internal/nut"
	"github.com/sweeney/ups-mqtt/internal/publisher"
)

var testCfg = &config.Config{
	NUT:  config.NUTConfig{UPSName: "cyberpower"},
	MQTT: config.MQTTConfig{TopicPrefix: "ups", Retained: true},
}

var labelledCfg = &config.Config{
	NUT:  config.NUTConfig{UPSName: "apc", Label: "office-ups"},
	MQTT: config.MQTTConfig{TopicPrefix: "ups", Retained: true},
}

var sampleVars = []nut.Variable{
	{Name: "ups.status", Value: "OL"},
	{Name: "ups.load", Value: "8"},
	{Name: "ups.realpower.nominal", Value: "900"},
	{Name: "battery.charge", Value: "100"},
	{Name: "battery.runtime", Value: "4920"},
	{Name: "input.voltage", Value: "242.0"},
	{Name: "input.voltage.nominal", Value: "230"},
}

var onBatteryVars = []nut.Variable{
	{Name: "ups.status", Value: "OB DISCHRG"},
	{Name: "ups.load", Value: "8"},
	{Name: "ups.realpower.nominal", Value: "900"},
	{Name: "battery.charge", Value: "100"},
	{Name: "battery.runtime", Value: "4090"},
}

func newOutageState() *outageState {
	return &outageState{}
}

// topicFailPublisher succeeds for every topic except failTopic, where it
// returns an error.  Used to exercise the outage-publish and outage-clear
// error paths in doPoll without affecting the PublishAll calls that precede them.
type topicFailPublisher struct {
	*publisher.FakePublisher
	failTopic string
}

func (t *topicFailPublisher) Publish(msg publisher.Message) error {
	if msg.Topic == t.failTopic {
		return errors.New("injected publish error")
	}
	return t.FakePublisher.Publish(msg)
}

func TestDoPoll_Success(t *testing.T) {
	fp := &nut.FakePoller{Variables: sampleVars}
	fpub := &publisher.FakePublisher{}
	outage := newOutageState()

	if err := doPoll(fp, fpub, testCfg, outage); err != nil {
		t.Fatalf("doPoll: %v", err)
	}
	if fp.CallCount != 1 {
		t.Errorf("CallCount = %d, want 1", fp.CallCount)
	}
	if _, ok := fpub.Find("ups/cyberpower/state"); !ok {
		t.Error("ups/cyberpower/state not published")
	}
	if _, ok := fpub.Find("ups/cyberpower/computed/status_display"); !ok {
		t.Error("ups/cyberpower/computed/status_display not published")
	}
	// Raw variables travel inside the state payload, not as their own topics.
	if _, ok := fpub.Find("ups/cyberpower/ups/status"); ok {
		t.Error("raw variable topics should no longer be published")
	}
}

func TestDoPoll_PollError(t *testing.T) {
	fp := &nut.FakePoller{Err: errors.New("connection lost")}
	fpub := &publisher.FakePublisher{}
	outage := newOutageState()

	err := doPoll(fp, fpub, testCfg, outage)
	if err == nil {
		t.Fatal("expected error when Poll fails")
	}
	if len(fpub.Messages) != 0 {
		t.Error("no messages should be published when Poll fails")
	}
}

func TestDoPoll_PublishError(t *testing.T) {
	fp := &nut.FakePoller{Variables: sampleVars}
	fpub := &publisher.FakePublisher{PublishError: errors.New("broker down")}
	outage := newOutageState()

	err := doPoll(fp, fpub, testCfg, outage)
	if err == nil {
		t.Fatal("expected error when publish fails")
	}
}

func TestDoPoll_OnBattery_SetsOutageStart(t *testing.T) {
	fp := &nut.FakePoller{Variables: onBatteryVars}
	fpub := &publisher.FakePublisher{}
	outage := newOutageState()

	if err := doPoll(fp, fpub, testCfg, outage); err != nil {
		t.Fatalf("doPoll: %v", err)
	}
	if outage.start == nil {
		t.Error("outage should be set after on-battery poll")
	}
	if _, ok := fpub.Find("ups/cyberpower/outage"); !ok {
		t.Error("outage topic not published")
	}
}

func TestDoPoll_OutageStart_NotResetOnSubsequentOnBatteryPoll(t *testing.T) {
	fp := &nut.FakePoller{Variables: onBatteryVars}
	fpub := &publisher.FakePublisher{}
	outage := newOutageState()

	// First poll — sets outage
	if err := doPoll(fp, fpub, testCfg, outage); err != nil {
		t.Fatalf("first poll: %v", err)
	}
	first := outage.start

	// Second poll — outage must remain the same timestamp
	fpub.Reset()
	if err := doPoll(fp, fpub, testCfg, outage); err != nil {
		t.Fatalf("second poll: %v", err)
	}
	if outage.start != first {
		t.Error("outage should not change between consecutive on-battery polls")
	}
}

func TestDoPoll_OutagePublishError_Propagated(t *testing.T) {
	fp := &nut.FakePoller{Variables: onBatteryVars}
	fpub := &topicFailPublisher{
		FakePublisher: &publisher.FakePublisher{},
		failTopic:     "ups/cyberpower/outage",
	}
	outage := newOutageState()

	err := doPoll(fp, fpub, testCfg, outage)
	if err == nil {
		t.Fatal("expected error when outage publish fails")
	}
}

func TestDoPoll_OutageClearError_Propagated(t *testing.T) {
	// Step 1: drive into on-battery state with a normal publisher.
	fp := &nut.FakePoller{Sequence: [][]nut.Variable{onBatteryVars, sampleVars}}
	outage := newOutageState()
	if err := doPoll(fp, &publisher.FakePublisher{}, testCfg, outage); err != nil {
		t.Fatalf("on-battery poll: %v", err)
	}

	// Step 2: power-restored poll with a publisher that fails on the outage topic.
	fpub := &topicFailPublisher{
		FakePublisher: &publisher.FakePublisher{},
		failTopic:     "ups/cyberpower/outage",
	}
	err := doPoll(fp, fpub, testCfg, outage)
	if err == nil {
		t.Fatal("expected error when outage clear fails")
	}
}

func TestDoPoll_Label_UsedInTopics(t *testing.T) {
	fp := &nut.FakePoller{Variables: sampleVars}
	fpub := &publisher.FakePublisher{}
	outage := newOutageState()

	if err := doPoll(fp, fpub, labelledCfg, outage); err != nil {
		t.Fatalf("doPoll: %v", err)
	}
	if _, ok := fpub.Find("ups/office-ups/state"); !ok {
		t.Error("state topic should use label, not ups_name")
	}
	if _, ok := fpub.Find("ups/apc/state"); ok {
		t.Error("state topic must not use ups_name when label is set")
	}
}

func TestDoPoll_Label_UsedInOutageTopic(t *testing.T) {
	fp := &nut.FakePoller{Variables: onBatteryVars}
	fpub := &publisher.FakePublisher{}
	outage := newOutageState()

	if err := doPoll(fp, fpub, labelledCfg, outage); err != nil {
		t.Fatalf("doPoll: %v", err)
	}
	if _, ok := fpub.Find("ups/office-ups/outage"); !ok {
		t.Error("outage topic should use label, not ups_name")
	}
	if _, ok := fpub.Find("ups/apc/outage"); ok {
		t.Error("outage topic must not use ups_name when label is set")
	}
}

func TestDoPoll_PowerRestored_ClearsOutage(t *testing.T) {
	fp := &nut.FakePoller{
		Sequence: [][]nut.Variable{onBatteryVars, sampleVars},
	}
	fpub := &publisher.FakePublisher{}
	outage := newOutageState()

	// Poll 1: on battery
	if err := doPoll(fp, fpub, testCfg, outage); err != nil {
		t.Fatalf("poll 1: %v", err)
	}
	if outage.start == nil {
		t.Fatal("outage should be set after on-battery poll")
	}

	// Poll 2: power restored
	fpub.Reset()
	if err := doPoll(fp, fpub, testCfg, outage); err != nil {
		t.Fatalf("poll 2: %v", err)
	}
	if outage.start != nil {
		t.Error("outage should be nil after power restored")
	}

	// Clear message: empty payload, retained, on outage topic
	msg, ok := fpub.Find("ups/cyberpower/outage")
	if !ok {
		t.Fatal("outage clear message not published")
	}
	if msg.Payload != "" {
		t.Errorf("clear message payload = %q, want empty", msg.Payload)
	}
	if !msg.Retained {
		t.Error("clear message should be retained")
	}
}

// ── Outage starvation: the outage message must never be gated behind the
//    ~50 telemetry topics, nor dropped because one of them failed ──────────

const (
	outageTopic = "ups/cyberpower/outage"
	stateTopic  = "ups/cyberpower/state"
)

// failAllExceptOutage fails every topic except the outage topic, simulating a
// link degraded badly enough that bulk telemetry cannot get through.
func failAllExceptOutage(topic string) bool { return topic != outageTopic }

// The outage message must be the FIRST thing on the wire, not the last.
func TestDoPoll_OutagePublishedBeforeTelemetry(t *testing.T) {
	fp := &publisher.FakePublisher{}
	poller := &nut.FakePoller{Variables: onBatteryVars}

	if err := doPoll(poller, fp, testCfg, newOutageState()); err != nil {
		t.Fatalf("doPoll: %v", err)
	}

	idx := fp.IndexOf(outageTopic)
	if idx == -1 {
		t.Fatal("outage topic was never published")
	}
	if idx != 0 {
		t.Errorf("outage published at position %d, want 0 (before all telemetry); first topic was %q",
			idx, fp.Messages[0].Topic)
	}
	if len(fp.Messages) < 5 {
		t.Fatalf("expected telemetry to follow the outage message, got %d messages", len(fp.Messages))
	}
}

// The regression this guards: previously PublishAll ran first and returned on
// its first error, so a single failed variable topic meant the outage message
// was never even attempted.
func TestDoPoll_OutageSurvivesTotalTelemetryFailure(t *testing.T) {
	fp := &publisher.FakePublisher{FailTopic: failAllExceptOutage}
	poller := &nut.FakePoller{Variables: onBatteryVars}

	err := doPoll(poller, fp, testCfg, newOutageState())
	if err == nil {
		t.Fatal("expected the telemetry failures to be reported")
	}

	msg, ok := fp.Find(outageTopic)
	if !ok {
		t.Fatal("outage message was NOT published despite telemetry failing — the alert was starved")
	}
	if !msg.Retained {
		t.Error("outage message must be retained")
	}
	if len(fp.Failed) == 0 {
		t.Error("expected telemetry topics to have been attempted and failed")
	}
}

// Every telemetry topic must still be attempted after one of them fails,
// rather than the loop abandoning the rest.
func TestDoPoll_OneFailedTopicDoesNotDropTheRest(t *testing.T) {
	failing := "ups/cyberpower/computed/load_watts"
	fp := &publisher.FakePublisher{FailTopic: func(tp string) bool { return tp == failing }}
	poller := &nut.FakePoller{Variables: sampleVars}

	// Outage topic already confirmed clear, so only telemetry is published.
	err := doPoll(poller, fp, testCfg, &outageState{clear: true})
	if err == nil {
		t.Fatal("expected the failed topic to be reported")
	}

	if _, ok := fp.Find(failing); ok {
		t.Errorf("%q should have failed, but was recorded as published", failing)
	}
	// The state topic is published last and is also the LWT topic: it is the
	// message most likely to be lost to an early return.
	if _, ok := fp.Find(stateTopic); !ok {
		t.Error("state topic was not published after an earlier topic failed")
	}
	// 7 computed - 1 failed = 6, plus the state topic.
	if len(fp.Messages) != 7 {
		t.Errorf("published %d messages, want 7; the remaining topics were dropped", len(fp.Messages))
	}
}

func TestDoPoll_TelemetryFailureStillReported(t *testing.T) {
	fp := &publisher.FakePublisher{FailTopic: failAllExceptOutage}
	poller := &nut.FakePoller{Variables: onBatteryVars}

	err := doPoll(poller, fp, testCfg, newOutageState())
	if err == nil {
		t.Fatal("telemetry failures must not be silently swallowed")
	}
}

// An outage-publish failure must not stop telemetry being attempted, and both
// errors must surface.
func TestDoPoll_BothFailures_BothReported(t *testing.T) {
	fp := &publisher.FakePublisher{FailTopic: func(string) bool { return true }}
	poller := &nut.FakePoller{Variables: onBatteryVars}

	err := doPoll(poller, fp, testCfg, newOutageState())
	if err == nil {
		t.Fatal("expected an error when everything fails")
	}
	if !strings.Contains(err.Error(), "outage") {
		t.Errorf("error should mention the outage failure, got: %v", err)
	}
	// Telemetry must have been attempted despite the outage publish failing.
	if len(fp.Failed) < 2 {
		t.Errorf("telemetry was not attempted after the outage publish failed; failed=%v", fp.Failed)
	}
}

func TestDoPoll_OutageFailure_TelemetryStillPublished(t *testing.T) {
	fp := &publisher.FakePublisher{FailTopic: func(tp string) bool { return tp == outageTopic }}
	poller := &nut.FakePoller{Variables: onBatteryVars}

	if err := doPoll(poller, fp, testCfg, newOutageState()); err == nil {
		t.Fatal("expected the outage failure to be reported")
	}
	if _, ok := fp.Find(stateTopic); !ok {
		t.Error("telemetry should still be published when the outage publish fails")
	}
}

// ── Outage clearing must be retried until it actually succeeds ────────────

// A failed clear previously still zeroed outage, so the retained outage
// message was left asserting an outage that had already ended.
func TestDoPoll_FailedClearIsRetried(t *testing.T) {
	start := time.Now().Add(-time.Minute)
	outage := &outageState{start: &start}

	fp := &publisher.FakePublisher{FailTopic: func(tp string) bool { return tp == outageTopic }}
	poller := &nut.FakePoller{Variables: sampleVars} // back on mains

	if err := doPoll(poller, fp, testCfg, outage); err == nil {
		t.Fatal("expected the clear failure to be reported")
	}
	if outage.start == nil {
		t.Fatal("outage was cleared despite the clear failing — the retained outage message is now orphaned")
	}

	// Second poll with the link healthy: the clear must be retried.
	fp2 := &publisher.FakePublisher{}
	if err := doPoll(poller, fp2, testCfg, outage); err != nil {
		t.Fatalf("retry poll: %v", err)
	}
	if outage.start != nil {
		t.Error("outage should be nil after a successful clear")
	}
	msg, ok := fp2.Find(outageTopic)
	if !ok {
		t.Fatal("clear was not retried on the following poll")
	}
	if msg.Payload != "" {
		t.Errorf("clear payload = %q, want empty (clears the retained message)", msg.Payload)
	}
}

func TestDoPoll_SuccessfulClear_ClearsOutageStart(t *testing.T) {
	start := time.Now().Add(-time.Minute)
	outage := &outageState{start: &start}

	fp := &publisher.FakePublisher{}
	poller := &nut.FakePoller{Variables: sampleVars}

	if err := doPoll(poller, fp, testCfg, outage); err != nil {
		t.Fatalf("doPoll: %v", err)
	}
	if outage.start != nil {
		t.Error("outage should be nil after a successful clear")
	}
}

// On mains, the outage topic is cleared once — on the first poll, in case a
// previous process left an outage retained — and then left alone.
func TestDoPoll_OnMains_ClearsOutageOnceAtStartup(t *testing.T) {
	fp := &publisher.FakePublisher{}
	poller := &nut.FakePoller{Variables: sampleVars}
	outage := newOutageState()

	if err := doPoll(poller, fp, testCfg, outage); err != nil {
		t.Fatalf("first poll: %v", err)
	}
	msg, ok := fp.Find(outageTopic)
	if !ok {
		t.Fatal("first poll on mains should clear the outage topic")
	}
	if msg.Payload != "" || !msg.Retained {
		t.Errorf("clear = {Payload: %q, Retained: %v}, want an empty retained payload", msg.Payload, msg.Retained)
	}

	fp.Reset()
	for i := range 3 {
		if err := doPoll(poller, fp, testCfg, outage); err != nil {
			t.Fatalf("poll %d: %v", i+2, err)
		}
	}
	if _, ok := fp.Find(outageTopic); ok {
		t.Error("outage topic should not be touched again once it is known clear")
	}
}

// A startup clear that fails must be retried, like any other clear.
func TestDoPoll_FailedStartupClearIsRetried(t *testing.T) {
	poller := &nut.FakePoller{Variables: sampleVars}
	outage := newOutageState()

	fp := &publisher.FakePublisher{FailTopic: func(tp string) bool { return tp == outageTopic }}
	if err := doPoll(poller, fp, testCfg, outage); err == nil {
		t.Fatal("expected the clear failure to be reported")
	}

	fp2 := &publisher.FakePublisher{}
	if err := doPoll(poller, fp2, testCfg, outage); err != nil {
		t.Fatalf("retry poll: %v", err)
	}
	if msg, ok := fp2.Find(outageTopic); !ok || msg.Payload != "" {
		t.Error("startup clear was not retried on the following poll")
	}
}

// An outage that ends must be cleared even if the topic was already
// confirmed clear before it began.
func TestDoPoll_OutageAfterStartupClear_IsClearedAgain(t *testing.T) {
	poller := &nut.FakePoller{Sequence: [][]nut.Variable{sampleVars, onBatteryVars, sampleVars}}
	fp := &publisher.FakePublisher{}
	outage := newOutageState()

	for i := range 3 {
		if err := doPoll(poller, fp, testCfg, outage); err != nil {
			t.Fatalf("poll %d: %v", i+1, err)
		}
	}
	if msg, ok := fp.Retained(outageTopic); ok {
		t.Errorf("outage still retained after mains restored: %s", msg.Payload)
	}
}

// ── The iPlug's firmware does not measure load ─────────────────────────────

// Its constant zero load must publish nothing, rather than a load_watts of 0
// that reads downstream as nothing plugged in.  Driven from the captured wire
// fixture, through doPoll, to what reaches the broker.
func TestDoPoll_RielloIPlug_PublishesNoLoad(t *testing.T) {
	fp := &publisher.FakePublisher{}
	poller := &nut.FakePoller{Variables: loadFixture(t, "listvar_riello.txt")}

	if err := doPoll(poller, fp, rielloCfg, newOutageState()); err != nil {
		t.Fatalf("doPoll: %v", err)
	}
	if msg, ok := fp.Find("ups/riello-ups/computed/load_watts"); ok {
		t.Errorf("computed/load_watts published as %q; the iPlug cannot measure load", msg.Payload)
	}

	state, ok := fp.Find(rielloStateTopic)
	if !ok {
		t.Fatal("state not published")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(state.Payload), &raw); err != nil {
		t.Fatalf("decoding state: %v", err)
	}
	var vars map[string]string
	var computed map[string]any
	if err := json.Unmarshal(raw["variables"], &vars); err != nil {
		t.Fatalf("decoding variables: %v", err)
	}
	if err := json.Unmarshal(raw["computed"], &computed); err != nil {
		t.Fatalf("decoding computed: %v", err)
	}
	for _, name := range []string{"ups.load", "output.power.percent", "output.L1.realpower", "output.L1.power", "output.L1.current"} {
		if v, ok := vars[name]; ok {
			t.Errorf("state.variables[%q] = %q; should be absent", name, v)
		}
	}
	if v, ok := computed["load_watts"]; ok {
		t.Errorf("state.computed.load_watts = %v; should be absent", v)
	}
	// What the device does measure still gets through.
	if vars["input.voltage"] != "251" || computed["status_display"] != "Online" {
		t.Errorf("real readings lost: input.voltage=%q status_display=%v", vars["input.voltage"], computed["status_display"])
	}
}
