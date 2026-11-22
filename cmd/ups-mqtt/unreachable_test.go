package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sweeney/ups-mqtt/internal/nut"
	"github.com/sweeney/ups-mqtt/internal/outages"
	"github.com/sweeney/ups-mqtt/internal/publisher"
)

// On 2026-11-19 lenin's CyberPower left the USB bus.  upsd answered every
// sample with DRIVER-NOT-CONNECTED for four days, and the daemon, alive and
// connected, published nothing: no LWT could fire, and the retained state
// sat on its last OL 100 % with only the timestamp to say it was old.
var errDriverGone = errors.New(`listing variables for "cyberpower": upsd error: DRIVER-NOT-CONNECTED`)

// step runs one sample at t0+at, the UPS answering or failing with err, the
// tracker's clock kept in step.
func step(d *daemon, clk *outages.FakeClock, t0 time.Time, at time.Duration, err error) {
	advanceTo(clk, t0.Add(at))
	d.poller.(*nut.FakePoller).Err = err
	d.sample(t0.Add(at), false)
}

// states decodes every payload published on the state topic, in order.
func states(t *testing.T, pub *publisher.FakePublisher) []map[string]any {
	t.Helper()
	var got []map[string]any
	for _, m := range pub.Messages {
		if m.Topic != sampleStateTopic {
			continue
		}
		var s map[string]any
		if err := json.Unmarshal([]byte(m.Payload), &s); err != nil {
			t.Fatalf("state %s is not JSON: %v", m.Payload, err)
		}
		got = append(got, s)
	}
	return got
}

// reachableOf summarises states as their ups_reachable values.
func reachableOf(ss []map[string]any) []any {
	var got []any
	for _, s := range ss {
		got = append(got, s["ups_reachable"])
	}
	return got
}

func equal(a, b []any) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Once NUT has failed for a poll interval the state topic says the UPS
// cannot be read, then repeats it every poll interval as the heartbeat would
// have been.  unreachable_since is the first failed sample's time.
func TestSample_UnreachableOnceNUTHasFailedForAPoll(t *testing.T) {
	d, pub, clk := samplingDaemon(reading("OL", "100"))
	t0 := clk.Now()

	step(d, clk, t0, 0, nil)
	for at := 2 * time.Second; at <= 22*time.Second; at += 2 * time.Second {
		step(d, clk, t0, at, errDriverGone)
	}

	ss := states(t, pub)
	// 0 s the reading; failing from 2 s, so unreachable at 12 s and 22 s.
	if got, want := reachableOf(ss), []any{true, false, false}; !equal(got, want) {
		t.Fatalf("ups_reachable on each state = %v, want %v", got, want)
	}
	u := ss[1]
	if u["error"] != errDriverGone.Error() {
		t.Errorf("error = %v, want %q", u["error"], errDriverGone)
	}
	ts, _ := time.Parse(time.RFC3339, u["timestamp"].(string))
	since, _ := time.Parse(time.RFC3339, u["unreachable_since"].(string))
	if got := ts.Sub(since); got != 10*time.Second {
		t.Errorf("unreachable_since is %v before the timestamp, want 10s (failing since 2 s, published at 12 s)", got)
	}
	ts2, _ := time.Parse(time.RFC3339, ss[2]["timestamp"].(string))
	since2, _ := time.Parse(time.RFC3339, ss[2]["unreachable_since"].(string))
	if got := ts2.Sub(since2); got != 20*time.Second {
		t.Errorf("second unreachable_since is %v before its timestamp, want 20s", got)
	}
}

// A process that starts with NUT already failing says so after a poll
// interval too, rather than leaving its predecessor's last state retained.
func TestSample_UnreachableFromTheFirstSample(t *testing.T) {
	d, pub, clk := samplingDaemon(reading("OL", "100"))
	t0 := clk.Now()
	for at := time.Duration(0); at <= 10*time.Second; at += 2 * time.Second {
		step(d, clk, t0, at, errDriverGone)
	}
	if got, want := reachableOf(states(t, pub)), []any{false}; !equal(got, want) {
		t.Errorf("ups_reachable on each state = %v, want %v", got, want)
	}
}

// A failed sample or two, a upsd restart or a re-dial, is not an outage of
// the UPS: Poll already re-dials once, and the next sample usually answers.
func TestSample_ABriefFailureIsNotUnreachable(t *testing.T) {
	d, pub, clk := samplingDaemon(reading("OL", "100"))
	t0 := clk.Now()
	step(d, clk, t0, 0, nil)
	step(d, clk, t0, 2*time.Second, errDriverGone)
	step(d, clk, t0, 4*time.Second, errDriverGone)
	for at := 6 * time.Second; at <= 20*time.Second; at += 2 * time.Second {
		step(d, clk, t0, at, nil)
	}
	if got, want := reachableOf(states(t, pub)), []any{true, true, true}; !equal(got, want) {
		t.Errorf("ups_reachable on each state = %v, want %v (0, 10, 20 s)", got, want)
	}
}

// When NUT answers again the reading goes out at once, not at the next poll:
// until it does, the retained state still says the UPS cannot be read.
func TestSample_RecoveryIsPublishedAtOnce(t *testing.T) {
	d, pub, clk := samplingDaemon(reading("OL", "100"))
	t0 := clk.Now()
	step(d, clk, t0, 0, nil)
	for at := 2 * time.Second; at <= 12*time.Second; at += 2 * time.Second {
		step(d, clk, t0, at, errDriverGone)
	}
	step(d, clk, t0, 14*time.Second, nil) // 2 s after the unreachable state

	if got, want := reachableOf(states(t, pub)), []any{true, false, true}; !equal(got, want) {
		t.Errorf("ups_reachable on each state = %v, want %v", got, want)
	}
	// And the publish rate is back to the poll's from there.
	step(d, clk, t0, 16*time.Second, nil)
	if got := len(states(t, pub)); got != 3 {
		t.Errorf("%d states after a sample 2 s on, want still 3", got)
	}
}

// A reconnect republishes current state at once (publishes made while the
// link was down were lost); while the UPS is unreachable, that is the
// unreachable state.  Before a poll interval of failures there is nothing to
// say yet, so nothing is published.
func TestSample_ReconnectRepublishesUnreachable(t *testing.T) {
	d, pub, clk := samplingDaemon(reading("OL", "100"))
	t0 := clk.Now()
	step(d, clk, t0, 0, nil)
	step(d, clk, t0, 2*time.Second, errDriverGone)
	advanceTo(clk, t0.Add(3*time.Second))
	d.sample(t0.Add(3*time.Second), true) // reconnect, failing for 1 s
	if got := len(states(t, pub)); got != 1 {
		t.Fatalf("%d states after a reconnect 1 s into a failure, want 1 (the reading)", got)
	}

	for at := 4 * time.Second; at <= 12*time.Second; at += 2 * time.Second {
		step(d, clk, t0, at, errDriverGone)
	}
	advanceTo(clk, t0.Add(13*time.Second))
	d.sample(t0.Add(13*time.Second), true) // reconnect, 1 s after the unreachable state

	if got, want := reachableOf(states(t, pub)), []any{true, false, false}; !equal(got, want) {
		t.Errorf("ups_reachable on each state = %v, want %v", got, want)
	}
}

// Without a reading the power state is unknown, so the outage topic is left
// as it is: neither cleared nor re-asserted.  A UPS on battery that becomes
// unreachable keeps its retained outage, which is the last thing known.
func TestSample_UnreachableLeavesTheOutageTopicAlone(t *testing.T) {
	d, pub, clk := samplingDaemon(reading("OB", "80"))
	t0 := clk.Now()
	step(d, clk, t0, 0, nil)
	before := count(pub, sampleOutageTopic)
	for at := 2 * time.Second; at <= 30*time.Second; at += 2 * time.Second {
		step(d, clk, t0, at, errDriverGone)
	}
	if got := count(pub, sampleOutageTopic); got != before {
		t.Errorf("outage topic published %d times while unreachable", got-before)
	}
	if got, ok := pub.Retained(sampleOutageTopic); !ok || got.Payload == "" {
		t.Errorf("retained outage = %q, %v: want the outage still asserted", got.Payload, ok)
	}
}

// The journal says when the daemon declared the UPS unreachable, once, so
// the moment is findable among the poll errors.
func TestSample_UnreachableIsLoggedOnce(t *testing.T) {
	logs := captureLogs(t)
	d, _, clk := samplingDaemon(reading("OL", "100"))
	t0 := clk.Now()
	step(d, clk, t0, 0, nil)
	for at := 2 * time.Second; at <= 40*time.Second; at += 2 * time.Second {
		step(d, clk, t0, at, errDriverGone)
	}
	got := lines(logs, "unreachable")
	if len(got) != 1 {
		t.Fatalf("logged %d unreachable lines, want 1:\n%s", len(got), strings.Join(got, "\n"))
	}
	if !strings.Contains(got[0], "ups_reachable:false") {
		t.Errorf("log %q does not say what was published", got[0])
	}
}

// advanceTo moves the tracker's clock, wall and monotonic, forward to t.
func advanceTo(clk *outages.FakeClock, t time.Time) {
	if d := t.Sub(clk.Now()); d > 0 {
		clk.Advance(d)
	}
}

// A second failure is timed from its own start, not the first's: it waits
// its own poll interval, and its unreachable_since is when it began.
func TestSample_ASecondFailureCountsFromItsOwnStart(t *testing.T) {
	d, pub, clk := samplingDaemon(reading("OL", "100"))
	t0 := clk.Now()
	step(d, clk, t0, 0, nil)
	for at := 2 * time.Second; at <= 12*time.Second; at += 2 * time.Second {
		step(d, clk, t0, at, errDriverGone) // unreachable at 12 s
	}
	for at := 14 * time.Second; at <= 30*time.Second; at += 2 * time.Second {
		step(d, clk, t0, at, nil) // answering: 14 s and 24 s
	}
	for at := 32 * time.Second; at <= 40*time.Second; at += 2 * time.Second {
		step(d, clk, t0, at, errDriverGone)
	}
	if got, want := reachableOf(states(t, pub)), []any{true, false, true, true}; !equal(got, want) {
		t.Fatalf("by 40 s, 8 s into the second failure, ups_reachable on each state = %v, want %v", got, want)
	}
	step(d, clk, t0, 42*time.Second, errDriverGone)
	ss := states(t, pub)
	if got, want := reachableOf(ss), []any{true, false, true, true, false}; !equal(got, want) {
		t.Fatalf("ups_reachable on each state = %v, want %v", got, want)
	}
	ts, _ := time.Parse(time.RFC3339, ss[4]["timestamp"].(string))
	since, _ := time.Parse(time.RFC3339, ss[4]["unreachable_since"].(string))
	if got := ts.Sub(since); got != 10*time.Second {
		t.Errorf("unreachable_since is %v before the timestamp, want 10s (since 32 s)", got)
	}
}
