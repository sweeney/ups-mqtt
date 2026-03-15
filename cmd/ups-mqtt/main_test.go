package main

import (
	"errors"
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

func newOutageStart() **time.Time {
	var p *time.Time
	return &p
}

func TestDoPoll_Success(t *testing.T) {
	fp := &nut.FakePoller{Variables: sampleVars}
	fpub := &publisher.FakePublisher{}
	outageStart := newOutageStart()

	if err := doPoll(fp, fpub, testCfg, outageStart); err != nil {
		t.Fatalf("doPoll: %v", err)
	}
	if fp.CallCount != 1 {
		t.Errorf("CallCount = %d, want 1", fp.CallCount)
	}
	if _, ok := fpub.Find("ups/cyberpower/ups/status"); !ok {
		t.Error("ups/cyberpower/ups/status not published")
	}
	if _, ok := fpub.Find("ups/cyberpower/state"); !ok {
		t.Error("ups/cyberpower/state not published")
	}
}

func TestDoPoll_PollError(t *testing.T) {
	fp := &nut.FakePoller{Err: errors.New("connection lost")}
	fpub := &publisher.FakePublisher{}
	outageStart := newOutageStart()

	err := doPoll(fp, fpub, testCfg, outageStart)
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
	outageStart := newOutageStart()

	err := doPoll(fp, fpub, testCfg, outageStart)
	if err == nil {
		t.Fatal("expected error when publish fails")
	}
}

func TestDoPoll_OnBattery_SetsOutageStart(t *testing.T) {
	fp := &nut.FakePoller{Variables: onBatteryVars}
	fpub := &publisher.FakePublisher{}
	outageStart := newOutageStart()

	if err := doPoll(fp, fpub, testCfg, outageStart); err != nil {
		t.Fatalf("doPoll: %v", err)
	}
	if *outageStart == nil {
		t.Error("outageStart should be set after on-battery poll")
	}
	if _, ok := fpub.Find("ups/cyberpower/outage"); !ok {
		t.Error("outage topic not published")
	}
}

func TestDoPoll_OutageStart_NotResetOnSubsequentOnBatteryPoll(t *testing.T) {
	fp := &nut.FakePoller{Variables: onBatteryVars}
	fpub := &publisher.FakePublisher{}
	outageStart := newOutageStart()

	// First poll — sets outageStart
	if err := doPoll(fp, fpub, testCfg, outageStart); err != nil {
		t.Fatalf("first poll: %v", err)
	}
	first := *outageStart

	// Second poll — outageStart must remain the same timestamp
	fpub.Reset()
	if err := doPoll(fp, fpub, testCfg, outageStart); err != nil {
		t.Fatalf("second poll: %v", err)
	}
	if *outageStart != first {
		t.Error("outageStart should not change between consecutive on-battery polls")
	}
}

func TestDoPoll_PowerRestored_ClearsOutage(t *testing.T) {
	fp := &nut.FakePoller{
		Sequence: [][]nut.Variable{onBatteryVars, sampleVars},
	}
	fpub := &publisher.FakePublisher{}
	outageStart := newOutageStart()

	// Poll 1: on battery
	if err := doPoll(fp, fpub, testCfg, outageStart); err != nil {
		t.Fatalf("poll 1: %v", err)
	}
	if *outageStart == nil {
		t.Fatal("outageStart should be set after on-battery poll")
	}

	// Poll 2: power restored
	fpub.Reset()
	if err := doPoll(fp, fpub, testCfg, outageStart); err != nil {
		t.Fatalf("poll 2: %v", err)
	}
	if *outageStart != nil {
		t.Error("outageStart should be nil after power restored")
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
