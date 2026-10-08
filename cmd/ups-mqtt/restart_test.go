package main

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/sweeney/ups-mqtt/internal/config"
	"github.com/sweeney/ups-mqtt/internal/nut"
	"github.com/sweeney/ups-mqtt/internal/publisher"
)

// ── A daemon that dies mid-outage must not leave the outage asserted ──────
//
// Seen in production on the Riello iPlug: mains failed, the daemon published
// one outage message (outage_duration_secs 0), and then the host lost power
// too, so the daemon never saw the recovery.  When mains came back the host
// booted, a fresh daemon found the UPS online — and published telemetry
// saying so every poll — while the broker went on handing every subscriber
// the retained "On Battery" message from the previous lifetime.
//
// The clear was only ever sent on an on-battery → on-mains transition that
// the same process had witnessed.  A new process starts with no memory of
// the outage, so it never cleared it.

var rielloCfg = &config.Config{
	NUT:  config.NUTConfig{UPSName: "iplug", Label: "riello-ups"},
	MQTT: config.MQTTConfig{TopicPrefix: "ups", Retained: true},
}

const (
	rielloOutageTopic = "ups/riello-ups/outage"
	rielloStateTopic  = "ups/riello-ups/state"
)

// loadFixture reads a captured upsd LIST VAR reply from internal/nut/testdata
// and returns its variables, in wire order, exactly as the device sent them.
func loadFixture(t *testing.T, name string) []nut.Variable {
	t.Helper()
	raw, err := os.ReadFile("../../internal/nut/testdata/" + name)
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	var vars []nut.Variable
	for _, line := range strings.Split(string(raw), "\n") {
		// VAR <ups> <name> "<value>"
		fields := strings.SplitN(line, " ", 4)
		if len(fields) != 4 || fields[0] != "VAR" {
			continue
		}
		value, err := strconv.Unquote(fields[3])
		if err != nil {
			t.Fatalf("fixture line %q: %v", line, err)
		}
		vars = append(vars, nut.Variable{Name: fields[2], Value: value})
	}
	if len(vars) == 0 {
		t.Fatalf("fixture %s held no variables", name)
	}
	return vars
}

// withOverrides returns a copy of vars with the named values replaced.
func withOverrides(vars []nut.Variable, overrides map[string]string) []nut.Variable {
	out := make([]nut.Variable, len(vars))
	for i, v := range vars {
		if o, ok := overrides[v.Name]; ok {
			v.Value = o
		}
		out[i] = v
	}
	return out
}

func TestRestartAfterOutage_ClearsStaleRetainedOutage(t *testing.T) {
	online := loadFixture(t, "listvar_riello.txt")
	// The iPlug on battery, as its outage message reported it: status OB,
	// battery sagging to 13.2 V, no load.
	onBattery := withOverrides(online, map[string]string{
		"ups.status":      "OB",
		"battery.voltage": "13.2",
	})

	// One broker, outliving both daemon processes.
	broker := &publisher.FakePublisher{}

	// ── Process 1: mains fail; one on-battery poll, then the host dies ──
	first := newOutageState()
	poller := &nut.FakePoller{Variables: onBattery}
	if err := doPoll(poller, broker, rielloCfg, first); err != nil {
		t.Fatalf("process 1 poll: %v", err)
	}

	outage, ok := broker.Retained(rielloOutageTopic)
	if !ok {
		t.Fatal("process 1 should have left an outage message retained")
	}
	var om publisher.OutageMessage
	if err := json.Unmarshal([]byte(outage.Payload), &om); err != nil {
		t.Fatalf("decoding outage: %v", err)
	}
	if om.Status != "OB" || om.StatusDisplay != "On Battery" || om.OutageDurationSecs != 0 {
		t.Fatalf("outage = %+v, want the single OB message seen in production", om)
	}

	// The host lost power: no final poll, no offline announcement.  The
	// broker notices the dead connection and publishes the LWT.
	broker.Publish(publisher.Message{ //nolint:errcheck
		Topic:    rielloStateTopic,
		Payload:  publisher.FormatOffline(),
		Retained: true,
	})

	// ── Process 2: host boots on restored mains; a fresh daemon starts ──
	second := newOutageState()
	poller = &nut.FakePoller{Variables: online}
	for i := range 3 {
		if err := doPoll(poller, broker, rielloCfg, second); err != nil {
			t.Fatalf("process 2 poll %d: %v", i+1, err)
		}
	}

	// The new daemon is telling the world the UPS is online…
	state, ok := broker.Retained(rielloStateTopic)
	if !ok {
		t.Fatal("process 2 published no retained state")
	}
	var sm publisher.StateMessage
	if err := json.Unmarshal([]byte(state.Payload), &sm); err != nil {
		t.Fatalf("decoding state: %v", err)
	}
	if sm.Computed.OnBattery {
		t.Fatal("state should report the UPS on mains")
	}

	// …so the broker must not still be asserting the outage.
	if msg, ok := broker.Retained(rielloOutageTopic); ok {
		t.Errorf("stale outage still retained after restart on mains: %s", msg.Payload)
	}
}

// Restarting during an outage that is still in progress must leave it
// asserted: the fresh daemon must not clear it on the way back up.
func TestRestartDuringOutage_KeepsOutageAsserted(t *testing.T) {
	online := loadFixture(t, "listvar_riello.txt")
	onBattery := withOverrides(online, map[string]string{"ups.status": "OB"})
	broker := &publisher.FakePublisher{}

	if err := doPoll(&nut.FakePoller{Variables: onBattery}, broker, rielloCfg, newOutageState()); err != nil {
		t.Fatalf("process 1 poll: %v", err)
	}

	// Process 2 starts while still on battery.
	if err := doPoll(&nut.FakePoller{Variables: onBattery}, broker, rielloCfg, newOutageState()); err != nil {
		t.Fatalf("process 2 poll: %v", err)
	}
	for _, m := range broker.Messages {
		if m.Topic == rielloOutageTopic && m.Payload == "" {
			t.Fatal("outage was cleared while the UPS was still on battery")
		}
	}
	if _, ok := broker.Retained(rielloOutageTopic); !ok {
		t.Error("outage should still be retained while on battery")
	}
}
