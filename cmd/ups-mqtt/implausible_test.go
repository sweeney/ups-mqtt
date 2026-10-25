package main

import (
	"bytes"
	"encoding/json"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/sweeney/ups-mqtt/internal/nut"
	"github.com/sweeney/ups-mqtt/internal/publisher"
)

// garbled is a Riello iPlug reading after its driver began misdecoding the
// battery fields: a percentage of 3434 and 38 days of runtime, which the
// device had never reported at all before.  The first on-battery reading
// also carried input.voltage 24346.
func garbled(status, voltage string) []nut.Variable {
	return []nut.Variable{
		{Name: "ups.status", Value: status},
		{Name: "battery.charge", Value: "3434"},
		{Name: "battery.runtime", Value: "3308760"},
		{Name: "battery.voltage", Value: "13.5"},
		{Name: "input.voltage", Value: voltage},
		{Name: "output.voltage.nominal", Value: "230"},
	}
}

// The 2026-10-23 outage, as the device reported it: impossible readings are
// left out of everything derived — computed topics, the outage message and
// last_outage — while state.variables still carries what the device sent.
func TestSample_ImpossibleReadingsDeriveNothing(t *testing.T) {
	var logs bytes.Buffer
	out, flags := log.Writer(), log.Flags()
	log.SetOutput(&logs)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(out); log.SetFlags(flags) })

	clean := []nut.Variable{
		{Name: "ups.status", Value: "OL"},
		{Name: "battery.voltage", Value: "13.5"},
		{Name: "input.voltage", Value: "245"},
		{Name: "output.voltage.nominal", Value: "230"},
	}
	d, pub, clk := samplingDaemon(
		clean,
		garbled("OB", "24346"),
		garbled("OL", "247"),
		garbled("OL", "246"),
		garbled("OL", "245"),
		garbled("OL", "245"),
		garbled("OL", "245"),
		clean, clean, clean, clean, clean, clean,
	)
	sampleAt(d, clk, time.Date(2026, 10, 23, 6, 28, 0, 0, time.UTC), 13)

	var msg publisher.Message // the outage, between the startup clear and its own
	for _, m := range pub.Messages {
		if m.Topic == sampleOutageTopic && m.Payload != "" {
			msg = m
			break
		}
	}
	if msg.Payload == "" {
		t.Fatal("outage not published")
	}
	for _, key := range []string{"battery_charge_pct", "battery_runtime_secs", "battery_runtime_mins", "estimated_depletion_at"} {
		if strings.Contains(msg.Payload, key) {
			t.Errorf("outage message carries %s from an impossible reading:\n%s", key, msg.Payload)
		}
	}

	last, ok := pub.Find(sampleLastTopic)
	if !ok {
		t.Fatal("last_outage not published")
	}
	var lo publisher.LastOutageMessage
	if err := json.Unmarshal([]byte(last.Payload), &lo); err != nil {
		t.Fatal(err)
	}
	if lo.MinBatteryChargePct != nil {
		t.Errorf("last_outage min_battery_charge_pct = %v, want absent", *lo.MinBatteryChargePct)
	}

	for _, m := range pub.Messages {
		if strings.HasSuffix(m.Topic, "/battery_runtime_mins") || strings.HasSuffix(m.Topic, "/battery_runtime_hours") {
			t.Errorf("%s published as %q from an impossible runtime", m.Topic, m.Payload)
		}
	}

	// Each state carries the device's raw reading, and derives from it only
	// what is possible: the voltage deviation once 24346 V has gone.
	var raws []string
	var deviations []bool
	for _, m := range pub.Messages {
		if m.Topic != sampleStateTopic {
			continue
		}
		var st struct {
			Variables map[string]string `json:"variables"`
			Computed  map[string]any    `json:"computed"`
		}
		if err := json.Unmarshal([]byte(m.Payload), &st); err != nil {
			t.Fatal(err)
		}
		raws = append(raws, st.Variables["battery.charge"])
		_, dev := st.Computed["input_voltage_deviation_pct"]
		deviations = append(deviations, dev)
		if _, ok := st.Computed["battery_runtime_mins"]; ok {
			t.Errorf("state.computed carries battery_runtime_mins: %s", m.Payload)
		}
	}
	if len(raws) < 3 || raws[1] != "3434" {
		t.Fatalf("state battery.charge per publish = %q, want the raw 3434 kept from the outage on", raws)
	}
	if deviations[1] || !deviations[2] {
		t.Errorf("input_voltage_deviation_pct present per publish = %v; want it absent only for 24346 V", deviations)
	}

	// Logged as the set of impossible readings changes, at most once a
	// 10 s poll: the change at 4 s waits until 12 s, the recovery at 14 s
	// until 22 s.
	want := []string{
		"ignoring impossible UPS readings: battery.charge=3434 battery.runtime=3308760 input.voltage=24346",
		"ignoring impossible UPS readings: battery.charge=3434 battery.runtime=3308760",
		"UPS readings back in range",
	}
	var got []string
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, "impossible") || strings.Contains(line, "in range") {
			got = append(got, line)
		}
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("logged:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A reading flapping in and out of range every sample is logged at most once
// a poll interval, not on every sample.
func TestSample_FlappingImpossibleReadingLoggedOncePerPoll(t *testing.T) {
	var logs bytes.Buffer
	out, flags := log.Writer(), log.Flags()
	log.SetOutput(&logs)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(out); log.SetFlags(flags) })

	bad := []nut.Variable{{Name: "ups.status", Value: "OL"}, {Name: "input.voltage", Value: "24346"}}
	good := []nut.Variable{{Name: "ups.status", Value: "OL"}, {Name: "input.voltage", Value: "245"}}
	var seq [][]nut.Variable
	for i := 0; i < 5; i++ { // 20 s: two poll intervals
		seq = append(seq, bad, good)
	}
	d, _, clk := samplingDaemon(seq...)
	sampleAt(d, clk, time.Date(2026, 10, 23, 6, 28, 0, 0, time.UTC), len(seq))

	if n := strings.Count(logs.String(), "impossible") + strings.Count(logs.String(), "in range"); n != 2 {
		t.Errorf("logged %d impossible/in-range lines over 20 s of flapping, want 2:\n%s", n, logs.String())
	}
}
