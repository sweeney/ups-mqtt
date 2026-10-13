package main

import (
	"strings"
	"testing"

	"github.com/sweeney/ups-mqtt/internal/config"
	"github.com/sweeney/ups-mqtt/internal/nut"
	"github.com/sweeney/ups-mqtt/internal/publisher"
)

// splitQOSCfg is the local-broker configuration: telemetry fire-and-forget,
// events queued by the bridge while its link is down.
func splitQOSCfg() *config.Config {
	telemetry, event := byte(0), byte(1)
	return &config.Config{
		NUT: config.NUTConfig{UPSName: "cyberpower"},
		MQTT: config.MQTTConfig{
			TopicPrefix: "ups", Retained: true, QOS: 1,
			TelemetryQOS: &telemetry, EventQOS: &event,
		},
	}
}

// assertQOS checks every recorded message: the outage topic at event, all
// else at telemetry.  It returns how many outage messages it saw.
func assertQOS(t *testing.T, fp *publisher.FakePublisher, telemetry, event byte) int {
	t.Helper()
	outages := 0
	for _, m := range fp.Messages {
		want := telemetry
		if m.Topic == outageTopic {
			want = event
			outages++
		}
		if m.QOS != want {
			t.Errorf("%s (payload %q…) published at QoS %d, want %d",
				m.Topic, truncate(m.Payload, 20), m.QOS, want)
		}
	}
	return outages
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// A whole outage — startup clear, on battery, restored — with split QoS:
// every outage publish and clear at event QoS, all telemetry at telemetry QoS.
func TestDoPoll_SplitQOS_EventsAndTelemetry(t *testing.T) {
	cfg := splitQOSCfg()
	fp := &publisher.FakePublisher{}
	outage := newOutageState()

	for _, vars := range [][]nut.Variable{sampleVars, onBatteryVars, onBatteryVars, sampleVars} {
		if err := doPoll(&nut.FakePoller{Variables: vars}, fp, cfg, outage); err != nil {
			t.Fatalf("doPoll() error: %v", err)
		}
	}

	// startup clear + 2 on-battery publishes + recovery clear
	if n := assertQOS(t, fp, 0, 1); n != 4 {
		t.Errorf("saw %d outage messages, want 4", n)
	}
	var sawState, sawComputed bool
	for _, m := range fp.Messages {
		sawState = sawState || m.Topic == "ups/cyberpower/state"
		sawComputed = sawComputed || strings.HasPrefix(m.Topic, "ups/cyberpower/computed/")
	}
	if !sawState || !sawComputed {
		t.Error("expected both state and computed telemetry to be published")
	}
}

// A config with only qos keeps publishing everything at it.
func TestDoPoll_SingleQOS_AppliesToEverything(t *testing.T) {
	cfg := &config.Config{
		NUT:  config.NUTConfig{UPSName: "cyberpower"},
		MQTT: config.MQTTConfig{TopicPrefix: "ups", Retained: true, QOS: 1},
	}
	fp := &publisher.FakePublisher{}
	outage := newOutageState()
	for _, vars := range [][]nut.Variable{onBatteryVars, sampleVars} {
		if err := doPoll(&nut.FakePoller{Variables: vars}, fp, cfg, outage); err != nil {
			t.Fatalf("doPoll() error: %v", err)
		}
	}
	assertQOS(t, fp, 1, 1)
}

// The shutdown announcement stands in for the LWT, so it is an event even
// though it shares the state topic with telemetry.
func TestPublishOffline_UsesEventQOS(t *testing.T) {
	cfg := splitQOSCfg()
	fp := &publisher.FakePublisher{}
	if err := publishOffline(fp, "ups/cyberpower/state", cfg.MQTT.EffectiveEventQOS()); err != nil {
		t.Fatalf("publishOffline() error: %v", err)
	}
	m, ok := fp.Retained("ups/cyberpower/state")
	if !ok {
		t.Fatal("offline announcement not retained on the state topic")
	}
	if m.QOS != 1 {
		t.Errorf("offline announcement at QoS %d, want event QoS 1", m.QOS)
	}
	if !strings.Contains(m.Payload, `"online":false`) {
		t.Errorf("payload = %s, want online:false", m.Payload)
	}
}
