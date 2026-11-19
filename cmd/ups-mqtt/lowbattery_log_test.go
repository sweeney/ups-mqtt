package main

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/sweeney/ups-mqtt/internal/nut"
)

// captureLogs sends the standard logger to a buffer for the test, without
// timestamps, and returns it.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	out, flags := log.Writer(), log.Flags()
	log.SetOutput(&logs)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(out); log.SetFlags(flags) })
	return &logs
}

// lines returns the logged lines containing substr.
func lines(logs *bytes.Buffer, substr string) []string {
	var got []string
	for _, l := range strings.Split(logs.String(), "\n") {
		if strings.Contains(l, substr) {
			got = append(got, l)
		}
	}
	return got
}

// riello is the Riello iPlug as it read during the 2026-11-18 run to empty
// (testdata/live-tests/2026-11-18): no real charge or runtime, only the
// misdecoded 3434 % and 38 days, so the battery voltage is all there is.
func riello(status, voltage string) []nut.Variable {
	return []nut.Variable{
		{Name: "ups.status", Value: status},
		{Name: "battery.charge", Value: "3434"},
		{Name: "battery.runtime", Value: "3308760"},
		{Name: "battery.voltage", Value: voltage},
		{Name: "input.voltage", Value: "4"},
	}
}

// The journal is where an outage is pieced together afterwards, and on
// 2026-11-18 it had no trace of the low battery that preceded the host's
// death: only outage.json and MQTT did.  Its onset is logged once, with the
// battery voltage, and never with a charge that cannot be real.
func TestSample_LogsLowBatteryOnset(t *testing.T) {
	logs := captureLogs(t)
	d, _, clk := samplingDaemon(
		riello("OB", "10.9"),
		riello("OB", "10.9"),
		riello("OB LB", "10.8"),
		riello("OB LB", "10.8"),
		riello("OB LB", "10.7"),
	)
	sampleAt(d, clk, time.Date(2026, 11, 18, 23, 11, 36, 0, time.UTC), 5)

	got := lines(logs, "low battery")
	if len(got) != 1 {
		t.Fatalf("low battery logged %d times, want once:\n%s", len(got), logs)
	}
	if !strings.Contains(got[0], "battery.voltage 10.8") {
		t.Errorf("log line %q does not give the battery voltage at onset", got[0])
	}
	if strings.Contains(got[0], "3434") {
		t.Errorf("log line %q repeats an impossible charge", got[0])
	}
}

// A real charge, where the UPS has one, is worth having in the line too.
func TestSample_LowBatteryLogGivesPlausibleCharge(t *testing.T) {
	logs := captureLogs(t)
	d, _, clk := samplingDaemon(reading("OB", "30"), reading("OB LB", "20"))
	sampleAt(d, clk, time.Date(2026, 11, 18, 23, 0, 0, 0, time.UTC), 2)

	got := lines(logs, "low battery")
	if len(got) != 1 || !strings.Contains(got[0], "battery.charge 20") {
		t.Errorf("want one low battery line giving battery.charge 20, got:\n%s", logs)
	}
}

// Low battery ending is logged too, whether mains returned or the load
// fell, so the journal shows how long the warning stood.
func TestSample_LogsLowBatteryCleared(t *testing.T) {
	logs := captureLogs(t)
	d, _, clk := samplingDaemon(
		riello("OB LB", "10.8"),
		riello("OB LB", "10.8"),
		riello("OB LB", "10.8"),
		riello("OB LB", "10.8"),
		riello("OB LB", "10.8"),
		riello("OB LB", "10.8"),
		riello("OL CHRG", "11.8"),
	)
	sampleAt(d, clk, time.Date(2026, 11, 18, 23, 26, 0, 0, time.UTC), 7)

	if got := lines(logs, "low battery cleared"); len(got) != 1 {
		t.Errorf("low battery cleared logged %d times, want once:\n%s", len(got), logs)
	}
}

// The onset is the line most wanted in the journal of a host about to die,
// so it is never held back behind a logged clear (PR #34 review).  Here a
// load spike raises LB mid-outage, it clears, and the real LB follows 4 s
// later: that is logged with the sample that saw it, not a poll later.
func TestSample_LowBatteryOnsetNotHeldBackByAClear(t *testing.T) {
	logs := captureLogs(t)
	seq := [][]nut.Variable{riello("OB LB", "11.0")} // the spike, t=0
	for range 5 {
		seq = append(seq, riello("OB", "11.1")) // t=2..10: clears; logged at 10
	}
	seq = append(seq, riello("OB", "11.0"), riello("OB LB", "10.8")) // t=12; the real LB at t=14
	d, _, clk := samplingDaemon(seq...)
	sampleAt(d, clk, time.Date(2026, 11, 18, 23, 11, 28, 0, time.UTC), len(seq))

	onsets := lines(logs, "low battery: ")
	if len(onsets) != 2 || !strings.Contains(onsets[1], "battery.voltage 10.8") {
		t.Errorf("want the spike and the real LB each logged as it came, got:\n%s", logs)
	}
}

// LB that flickers on every 2 s sample must not flood the journal: clears
// are logged at most once a poll interval, and an onset only after a logged
// clear, so at most two lines a poll interval.
func TestSample_FlappingLowBatteryLogsSparingly(t *testing.T) {
	logs := captureLogs(t)
	var seq [][]nut.Variable
	for i := range 15 { // 30 s, three poll intervals
		if i%2 == 0 {
			seq = append(seq, riello("OB LB", "10.8"))
		} else {
			seq = append(seq, riello("OB", "10.9"))
		}
	}
	d, _, clk := samplingDaemon(seq...)
	sampleAt(d, clk, time.Date(2026, 11, 18, 23, 11, 0, 0, time.UTC), len(seq))

	if got := lines(logs, "low battery"); len(got) == 0 || len(got) > 7 {
		t.Errorf("flapping LB logged %d lines over 30 s, want at most two a poll interval:\n%s", len(got), logs)
	}
}
