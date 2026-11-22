package publisher_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

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
// the last reading: on 2026-11-19 lenin's UPS left the USB bus and the
// retained state sat on a 4-day-old OL 100 % with nothing to say it was old
// but its timestamp.  The payload carries no variables or computed values,
// since there is no reading; it says since when, and why.
func TestPublishUnreachable_Shape(t *testing.T) {
	yes := true
	fp := &publisher.FakePublisher{}
	cfg := publisher.PublishConfig{Prefix: "ups", UPSName: "cyberpower", Retained: true,
		TelemetryQOS: 0, EventQOS: 1, ClockSynced: &yes}
	cause := errors.New(`listing variables for "cyberpower": upsd error: DRIVER-NOT-CONNECTED`)

	if err := publisher.PublishUnreachable(cfg, 95*time.Second, cause, fp); err != nil {
		t.Fatalf("PublishUnreachable: %v", err)
	}
	msg, got := unreachable(t, fp)

	if got["ups_reachable"] != false {
		t.Errorf("ups_reachable = %v, want false", got["ups_reachable"])
	}
	if got["ups_name"] != "cyberpower" {
		t.Errorf("ups_name = %v, want cyberpower", got["ups_name"])
	}
	if got["clock_synced"] != true {
		t.Errorf("clock_synced = %v, want true", got["clock_synced"])
	}
	if got["error"] != cause.Error() {
		t.Errorf("error = %v, want %q", got["error"], cause.Error())
	}
	for _, k := range []string{"variables", "computed", "online"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s present with no reading to report: %s", k, msg.Payload)
		}
	}
	ts, err := time.Parse(time.RFC3339, got["timestamp"].(string))
	if err != nil {
		t.Fatalf("timestamp: %v", err)
	}
	since, err := time.Parse(time.RFC3339, got["unreachable_since"].(string))
	if err != nil {
		t.Fatalf("unreachable_since: %v", err)
	}
	if d := ts.Sub(since); d != 95*time.Second {
		t.Errorf("unreachable_since is %v before timestamp, want 95s", d)
	}
	if !msg.Retained {
		t.Error("not retained: a late subscriber would get the last reading instead")
	}
	// It replaces the heartbeat every poll while NUT is down, so it is
	// telemetry: at QoS 1 it would fill a bridge's queue like any other.
	if msg.QOS != 0 {
		t.Errorf("QoS %d, want the telemetry QoS 0", msg.QOS)
	}
}

// Like the snapshot, clock_synced is absent where sync cannot be determined.
func TestPublishUnreachable_ClockUnknown(t *testing.T) {
	fp := &publisher.FakePublisher{}
	cfg := publisher.PublishConfig{Prefix: "ups", UPSName: "cyberpower"}
	if err := publisher.PublishUnreachable(cfg, time.Minute, errors.New("x"), fp); err != nil {
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
	if err := publisher.PublishUnreachable(cfg, time.Minute, errors.New("x"), fp); err == nil {
		t.Error("PublishUnreachable() = nil with the publish failing")
	}
}
