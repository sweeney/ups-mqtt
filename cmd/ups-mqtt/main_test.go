package main

import (
	"errors"
	"testing"

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

func TestDoPoll_Success(t *testing.T) {
	fp := &nut.FakePoller{Variables: sampleVars}
	fpub := &publisher.FakePublisher{}

	if err := doPoll(fp, fpub, testCfg); err != nil {
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

	err := doPoll(fp, fpub, testCfg)
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

	err := doPoll(fp, fpub, testCfg)
	if err == nil {
		t.Fatal("expected error when publish fails")
	}
}
