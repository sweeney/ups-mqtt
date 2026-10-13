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
	// The bridge retries every 1–2 s once the uplink is back; settle long
	// enough (live time) that its queue has drained before the run ends.
	r := replayOn(t, bridged, tl, faults, 2*time.Minute, cutoverQOS)
	checkOutcome(t, r, tl)

	if strings.Contains(r.logs, "MQTT connection to") {
		t.Errorf("ups-mqtt saw a connection problem; behind the bridge only the uplink failed:\n%s", r.logs)
	}

	// Every outage message published while the uplink was down was queued
	// and delivered: the main broker received exactly what the daemon
	// published on its local broker, in the same order, ending with the
	// clear.  (Payload contents cannot show order at this speed: the whole
	// outage lasts under a second.)
	sent, got := payloads(r.source.On(replayOutageTopic)), payloads(r.rec.On(replayOutageTopic))
	if len(sent) < 3 {
		t.Fatalf("daemon published %d outage messages, want an outage and its clear at least", len(sent))
	}
	if strings.Join(got, "\n") != strings.Join(sent, "\n") {
		t.Errorf("main broker received outage messages\n  %q\nwant, in the order published locally,\n  %q", got, sent)
	}

	// Telemetry is QoS 0 and is not queued: of the states published while
	// the link was down, all but a stray few never reach the main broker.
	// (Mosquitto can still deliver a QoS 0 message that arrives while the
	// bridge is mid-reconnect; at QoS 1 nearly every one is delivered.)
	// State timestamps are whole seconds; a second's margin each side.
	start := r.poller.start
	downAt, upAt := start.Add(scaled(down)+time.Second), start.Add(scaled(up)-time.Second)
	inWindow := func(ms []mqtttest.Received) int {
		n := 0
		for _, m := range ms {
			var s struct {
				Timestamp time.Time `json:"timestamp"`
			}
			if json.Unmarshal([]byte(m.Payload), &s) == nil && s.Timestamp.After(downAt) && s.Timestamp.Before(upAt) {
				n++
			}
		}
		return n
	}
	published, delivered := inWindow(r.source.On(replayStateTopic)), inWindow(r.rec.On(replayStateTopic))
	if published < 4 {
		t.Fatalf("only %d states published while the link was down; the window is too short to tell", published)
	}
	if delivered*4 > published {
		t.Errorf("%d of the %d states published while the link was down reached the main broker: telemetry is being queued",
			delivered, published)
	}

	// Whatever straggled in, the main broker ends on a current state.
	if p, ok := mqtttest.Retained(t, r.observe, replayStateTopic); ok {
		var s struct {
			Timestamp time.Time `json:"timestamp"`
		}
		if json.Unmarshal([]byte(p), &s) == nil && !s.Timestamp.IsZero() && s.Timestamp.Before(upAt) {
			t.Errorf("main broker retains a state from %s, before the link came back", s.Timestamp.Format(time.RFC3339))
		}
	}

	// The bridge reported the link going and coming back.
	var seen []string
	for _, m := range r.rec.On(mqtttest.BridgeNotificationTopic) {
		seen = append(seen, m.Payload)
	}
	notes := strings.Join(seen, ",")
	if i := strings.Index(notes, "0"); i < 0 || !strings.Contains(notes[i:], "1") {
		t.Errorf("bridge notifications %q, want the link reported down (0) and then back up (1)", notes)
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
	var first time.Time
	for _, m := range r.rec.On(replayOutageTopic) {
		if m.Payload != "" {
			first = m.At
			break
		}
	}
	if first.IsZero() {
		t.Fatal("no outage message reached the main broker")
	}
	if lag := first.Sub(r.poller.start.Add(scaled(start))); lag > scaled(6*time.Second) {
		t.Errorf("outage reached the main broker %s (live %s) after the UPS went on battery", lag, lag*compress)
	}
}

// payloads lists what was received, in order.
func payloads(ms []mqtttest.Received) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Payload
	}
	return out
}

// After the uplink returns, the main broker's notification topic says the
// bridge is up.  With `topic # out 1` it could end on a stale "0": the
// bridge also publishes its state on the local broker, the catch-all line
// queued that "0" while the uplink was down, and forwarded it after the
// reconnect, where on Mosquitto 2.0 it landed after the fresh "1".  The
// shipped config now forwards only the topics it means to.
func TestBridgedReplay_NotificationEndsUp(t *testing.T) {
	tl := timeline{{0, "OL"}, {15 * time.Second, "OB"}, {33 * time.Second, "OL"}, {90 * time.Second, "OL"}}
	r := replayOn(t, bridged, tl, []fault{{10 * time.Second, mqtttest.Down}, {40 * time.Second, mqtttest.Up}}, 2*time.Minute, cutoverQOS)
	if p, ok := mqtttest.Retained(t, r.observe, mqtttest.BridgeNotificationTopic); !ok || p != "1" {
		t.Errorf("main broker retains bridge state %q with the link up, want 1", p)
	}
	// The local notification itself is never forwarded.
	n := 0
	for _, m := range r.rec.On(mqtttest.BridgeNotificationTopic) {
		if m.Payload == "0" {
			n++
		}
	}
	if n > 1 {
		t.Errorf("main broker saw %d \"0\" notifications for one outage; the local one is being forwarded", n)
	}
}
