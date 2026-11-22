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

// A full snapshot says the UPS answered, so a subscriber can tell it from
// the unreachable shape by one field rather than by which fields are missing.
func TestPublishAll_StateSaysTheUPSIsReachable(t *testing.T) {
	fp := runPublishAll(t)
	msg, _ := fp.Find("ups/cyberpower/state")
	if !strings.Contains(msg.Payload, `"ups_reachable":true`) {
		t.Errorf("state lacks ups_reachable:true: %s", msg.Payload)
	}
}

// unreachable decodes the payload PublishUnreachable sent on the state topic.
func unreachable(t *testing.T, fp *publisher.FakePublisher) (publisher.Message, map[string]any) {
	t.Helper()
	msg, ok := fp.Find("ups/cyberpower/state")
	if !ok {
		t.Fatal("nothing published on the state topic")
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(msg.Payload), &got); err != nil {
		t.Fatalf("payload %s is not JSON: %v", msg.Payload, err)
	}
	return msg, got
}

// When NUT stops answering, the state topic says so instead of freezing on
// the last reading: on 2026-11-19 a host's UPS left its USB bus and the
// retained state sat on a 4-day-old OL 100 % with nothing to say it was old
// but its timestamp.  The payload carries no variables or computed values,
// since there is no reading; it says since when, and a coarse cause.
func TestPublishUnreachable_Shape(t *testing.T) {
	yes := true
	fp := &publisher.FakePublisher{}
	cfg := publisher.PublishConfig{Prefix: "ups", UPSName: "cyberpower", Retained: true,
		TelemetryQOS: 0, EventQOS: 1, ClockSynced: &yes}
	at := time.Date(2026, 11, 19, 15, 49, 33, 600_000_000, time.UTC)
	since := time.Date(2026, 11, 19, 15, 47, 58, 400_000_000, time.UTC)

	if err := publisher.PublishUnreachable(cfg, at, since, "DRIVER-NOT-CONNECTED", fp); err != nil {
		t.Fatalf("PublishUnreachable: %v", err)
	}
	msg, got := unreachable(t, fp)

	for k, want := range map[string]any{
		"ups_reachable":     false,
		"ups_name":          "cyberpower",
		"clock_synced":      true,
		"cause":             "DRIVER-NOT-CONNECTED",
		"timestamp":         "2026-11-19T15:49:33Z",
		"unreachable_since": "2026-11-19T15:47:58Z",
	} {
		if got[k] != want {
			t.Errorf("%s = %v, want %v", k, got[k], want)
		}
	}
	for _, k := range []string{"variables", "computed", "online", "error"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s present: %s", k, msg.Payload)
		}
	}
	if !msg.Retained {
		t.Error("not retained with mqtt.retained on: a late subscriber would get the last reading instead")
	}
	// It replaces the heartbeat every poll while NUT is down, so it is
	// telemetry: at QoS 1 it would fill a bridge's queue like any other.
	if msg.QOS != 0 {
		t.Errorf("QoS %d, want the telemetry QoS 0", msg.QOS)
	}
}

// It follows mqtt.retained, like the snapshot it stands in for.
func TestPublishUnreachable_FollowsRetained(t *testing.T) {
	fp := &publisher.FakePublisher{}
	cfg := publisher.PublishConfig{Prefix: "ups", UPSName: "cyberpower", TelemetryQOS: 1}
	if err := publisher.PublishUnreachable(cfg, time.Now(), time.Now(), "timeout", fp); err != nil {
		t.Fatal(err)
	}
	msg, _ := unreachable(t, fp)
	if msg.Retained || msg.QOS != 1 {
		t.Errorf("retained %v, QoS %d: want mqtt.retained (off) and the telemetry QoS (1)", msg.Retained, msg.QOS)
	}
}

// Like the snapshot, clock_synced is absent where sync cannot be determined.
func TestPublishUnreachable_ClockUnknown(t *testing.T) {
	fp := &publisher.FakePublisher{}
	cfg := publisher.PublishConfig{Prefix: "ups", UPSName: "cyberpower"}
	if err := publisher.PublishUnreachable(cfg, time.Now(), time.Now(), "x", fp); err != nil {
		t.Fatal(err)
	}
	msg, got := unreachable(t, fp)
	if _, ok := got["clock_synced"]; ok {
		t.Errorf("clock_synced present with sync unknown: %s", msg.Payload)
	}
}

// A failed publish is returned, so the caller can log it.
func TestPublishUnreachable_ReturnsPublishError(t *testing.T) {
	fp := &publisher.FakePublisher{PublishError: errors.New("broker gone")}
	cfg := publisher.PublishConfig{Prefix: "ups", UPSName: "cyberpower"}
	if err := publisher.PublishUnreachable(cfg, time.Now(), time.Now(), "x", fp); err == nil {
		t.Error("PublishUnreachable() = nil with the publish failing")
	}
}

// Without a reading the computed values are unknown, and unknown is absent:
// ClearComputed empties every retained computed/ topic, at the telemetry QoS.
func TestClearComputed(t *testing.T) {
	fp := &publisher.FakePublisher{}
	cfg := publisher.PublishConfig{Prefix: "ups", UPSName: "cyberpower", Retained: true, TelemetryQOS: 0, EventQOS: 1}
	if err := publisher.ClearComputed(cfg, fp); err != nil {
		t.Fatal(err)
	}
	if len(fp.Messages) != len(metrics.TopicNames) {
		t.Errorf("%d clears, want one per computed topic (%d)", len(fp.Messages), len(metrics.TopicNames))
	}
	for _, name := range metrics.TopicNames {
		topic := "ups/cyberpower/computed/" + name
		m, ok := fp.Find(topic)
		if !ok {
			t.Errorf("computed/%s not cleared", name)
			continue
		}
		if _, held := fp.Retained(topic); held {
			t.Errorf("computed/%s still held retained after the clear", name)
		}
		if m.Payload != "" || !m.Retained || m.QOS != 0 {
			t.Errorf("computed/%s: payload %q retained %v QoS %d, want an empty retained QoS 0", name, m.Payload, m.Retained, m.QOS)
		}
	}
}

// Every clear is attempted even when one fails, and the failures returned.
func TestClearComputed_AttemptsEveryTopic(t *testing.T) {
	fp := &publisher.FakePublisher{
		PublishError: errors.New("link degraded"),
		FailTopic:    func(topic string) bool { return strings.HasSuffix(topic, "/on_battery") },
	}
	cfg := publisher.PublishConfig{Prefix: "ups", UPSName: "cyberpower", Retained: true}
	if err := publisher.ClearComputed(cfg, fp); err == nil {
		t.Error("ClearComputed() = nil with a clear failing")
	}
	if got := len(fp.Messages) + len(fp.Failed); got != len(metrics.TopicNames) {
		t.Errorf("%d clears attempted, want %d", got, len(metrics.TopicNames))
	}
}
