package main

// The live tests replayed through the production bridge: ups-mqtt publishes
// to a local Mosquitto running the shipped config (internal/mqtttest/bridge),
// and it is the bridge's link to the main broker that fails.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sweeney/ups-mqtt/internal/config"
	"github.com/sweeney/ups-mqtt/internal/mqtttest"
)

// The production QoS since the cutover: telemetry 0, so the bridge does not
// queue it, and events 1, so it does.
func cutoverQOS(c *config.MQTTConfig) {
	zero, one := byte(0), byte(1)
	c.TelemetryQOS, c.EventQOS = &zero, &one
}

// Live test 1 behind the bridge: the uplink drops just before the cut and
// comes back two minutes after mains.  ups-mqtt never notices — its broker
// is local — and the main broker hears the whole outage once the link is
// back, in order, ending clear.
func TestBridgedReplay_NetworkUnpowered(t *testing.T) {
	tl := timeline{
		{0, "OL"},
		{15 * time.Second, "OB"},
		{33 * time.Second, "OL"},
		{2*time.Minute + 50*time.Second, "OL"},
	}
	down, up := 12*time.Second, 2*time.Minute+24*time.Second
	faults := []fault{{down, mqtttest.Down}, {up, mqtttest.Up}}
	r := replayOn(t, bridged, tl, faults, 15*time.Second, cutoverQOS)
	checkOutcome(t, r, tl)

	if strings.Contains(r.logs, "MQTT connection to") {
		t.Errorf("ups-mqtt saw a connection problem; behind the bridge only the uplink failed:\n%s", r.logs)
	}

	// Every outage message published while the uplink was down was queued
	// and delivered, in order, the clear last.
	outs := r.rec.On(replayOutageTopic)
	var durations []int64
	for _, m := range outs {
		if m.Payload == "" {
			continue
		}
		var o struct {
			Duration int64 `json:"outage_duration_secs"`
		}
		if err := json.Unmarshal([]byte(m.Payload), &o); err != nil {
			t.Fatalf("outage payload %q: %v", m.Payload, err)
		}
		durations = append(durations, o.Duration)
	}
	_, ob := tl.onBattery(t)
	if want := int(scaled(ob)/scaled(10*time.Second)) + 1; len(durations) < want {
		t.Errorf("%d outage messages reached the main broker, want the ≥%d published while its link was down", len(durations), want)
	}
	for i := 1; i < len(durations); i++ {
		if durations[i] < durations[i-1] {
			t.Errorf("outage messages out of order: %v", durations)
			break
		}
	}

	// Telemetry is QoS 0 and is not queued: nothing published while the
	// link was down reaches the main broker afterwards.  (State timestamps
	// are whole seconds; a second's margin each side.)
	start := r.poller.start
	downAt, upAt := start.Add(scaled(down)+time.Second), start.Add(scaled(up)-time.Second)
	for _, m := range r.rec.On(replayStateTopic) {
		var s struct {
			Timestamp time.Time `json:"timestamp"`
		}
		if json.Unmarshal([]byte(m.Payload), &s) == nil && s.Timestamp.After(downAt) && s.Timestamp.Before(upAt) {
			t.Errorf("state from %s, while the link was down, was queued and delivered", s.Timestamp.Format(time.RFC3339))
		}
	}

	// The bridge reported the link going and coming back.
	var seen []string
	for _, m := range r.rec.On(mqtttest.BridgeNotificationTopic) {
		seen = append(seen, m.Payload)
	}
	if got := strings.Join(seen, ","); !strings.Contains(got, "1,0") || !strings.HasSuffix(got, "1") {
		t.Errorf("bridge notifications %q, want the link reported down (0) and back up (1)", got)
	}
}

// The clean run behind the bridge: the outage reaches the main broker
// within a sample, through the local broker and the bridge.
func TestBridgedReplay_Clean(t *testing.T) {
	tl := loadTimeline(t, "3-clean", "20:04:30", "20:06:30")
	// Mains comes back at 20:06:30 in this shortened timeline.
	tl = append(tl, statusAt{at: tl.end() + time.Second, status: "OL"}, statusAt{at: tl.end() + 30*time.Second, status: "OL"})
	r := replayOn(t, bridged, tl, nil, 15*time.Second, cutoverQOS)
	checkOutcome(t, r, tl)

	start, _ := tl.onBattery(t)
	for _, m := range r.rec.On(replayOutageTopic) {
		if m.Payload == "" {
			continue
		}
		if lag := m.At.Sub(r.poller.start.Add(scaled(start))); lag > scaled(2*time.Second)+scaled(3*time.Second) {
			t.Errorf("outage reached the main broker %s (live %s) after the UPS went on battery", lag, lag*compress)
		}
		break
	}
}
