package main

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sweeney/ups-mqtt/internal/config"
	"github.com/sweeney/ups-mqtt/internal/nut"
	"github.com/sweeney/ups-mqtt/internal/outages"
	"github.com/sweeney/ups-mqtt/internal/publisher"
)

// ── The 2026-11-18 run to empty, replayed from the Riello's own bytes ─────
//
// docs/live-tests.md, 2026-11-18: mains failed at the Pi's site and stayed
// off until the Riello iPlug ran flat; the Pi died with it, and booted when
// mains returned with its clock 14½ min slow.  The UPS snapshots recorded
// then (upsd's LIST VAR replies, every 10 s and every 2 s from 23:10) are
// replayed here through the daemon as it samples, then a fresh process is
// started as the reboot started one.  The restart tests cover the same
// ground with a synthetic fixture; this pins it to what the device sent,
// misdecoded charge and runtime included.

const runToEmptyLog = "../../testdata/live-tests/2026-11-18/1-run-to-empty/ups-host-listvar.log"

// snapshot is one recorded LIST VAR reply and when it was taken.
type snapshot struct {
	at   time.Time
	vars []nut.Variable
}

// loadSnapshots reads a live-test listvar.log: "=== <RFC 3339 time>" then
// upsd's VAR lines, verbatim.
func loadSnapshots(t *testing.T, path string) []snapshot {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var snaps []snapshot
	for _, line := range strings.Split(string(raw), "\n") {
		if at, ok := strings.CutPrefix(line, "=== "); ok {
			ts, err := time.Parse(time.RFC3339Nano, at)
			if err != nil {
				t.Fatalf("snapshot header %q: %v", line, err)
			}
			snaps = append(snaps, snapshot{at: ts})
			continue
		}
		// VAR <ups> <name> "<value>"
		fields := strings.SplitN(line, " ", 4)
		if len(fields) != 4 || fields[0] != "VAR" || len(snaps) == 0 {
			continue
		}
		value, err := strconv.Unquote(fields[3])
		if err != nil {
			t.Fatalf("snapshot line %q: %v", line, err)
		}
		last := &snaps[len(snaps)-1]
		last.vars = append(last.vars, nut.Variable{Name: fields[2], Value: value})
	}
	if len(snaps) < 100 {
		t.Fatalf("%s: only %d snapshots", path, len(snaps))
	}
	return snaps
}

func varOf(vars []nut.Variable, name string) string {
	for _, v := range vars {
		if v.Name == name {
			return v.Value
		}
	}
	return ""
}

const (
	emptyRunOutageTopic = "ups/site-ups/outage"
	emptyRunLastTopic   = "ups/site-ups/last_outage"
	emptyRunStateTopic  = "ups/site-ups/state"
)

var emptyRunCfg = &config.Config{
	NUT: config.NUTConfig{
		UPSName:        "iplug",
		Label:          "site-ups",
		PollInterval:   config.Duration{Duration: 10 * time.Second},
		SampleInterval: config.Duration{Duration: 2 * time.Second},
	},
	MQTT: config.MQTTConfig{TopicPrefix: "ups", Retained: true},
}

func TestReplay_RunToEmpty_2026_11_18(t *testing.T) {
	logs := captureLogs(t)
	snaps := loadSnapshots(t, runToEmptyLog)
	broker := &publisher.FakePublisher{}
	store := &outages.MemoryStore{}

	// ── First lifetime: on battery until the host dies ───────────────────
	// The Pi had been up 35 days.
	bootA := -35 * 24 * time.Hour
	clk := &outages.FakeClock{Wall: snaps[0].at, Boot: &bootA, IsSynced: true, SyncKnown: true, BootIDValue: "boot-a"}
	seq := make([][]nut.Variable, len(snaps))
	for i, s := range snaps {
		seq[i] = s.vars
	}
	d := &daemon{cfg: emptyRunCfg, poller: &nut.FakePoller{Sequence: seq}, pub: broker,
		outage: outageState{tracker: outages.NewTracker(clk, store)}}

	firstLB := -1
	var lastOutageAt time.Time
	for i, s := range snaps {
		if i > 0 {
			clk.Advance(s.at.Sub(snaps[i-1].at))
		}
		before := len(broker.Messages)
		d.sample(s.at, false)

		var outage *publisher.Message
		for j := before; j < len(broker.Messages); j++ {
			if m := broker.Messages[j]; m.Topic == emptyRunOutageTopic && m.Payload != "" {
				outage = &broker.Messages[j]
			}
		}
		if outage != nil {
			// A poll is never missed: no more than a poll (plus a sample's
			// slack) between outage messages.
			if !lastOutageAt.IsZero() && s.at.Sub(lastOutageAt) > 12*time.Second {
				t.Errorf("no outage published from %s to %s", lastOutageAt.Format(time.TimeOnly), s.at.Format(time.TimeOnly))
			}
			lastOutageAt = s.at
			for _, key := range []string{"battery_charge_pct", "battery_runtime_secs", "estimated_depletion_at"} {
				if strings.Contains(outage.Payload, key) {
					t.Fatalf("%s: outage carries %s from the misdecoded reading:\n%s", s.at.Format(time.TimeOnly), key, outage.Payload)
				}
			}
		}

		if firstLB < 0 && strings.Contains(varOf(s.vars, "ups.status"), "LB") {
			firstLB = i
			// The first low battery goes out with the sample that saw it.
			if outage == nil || !strings.Contains(outage.Payload, `"low_battery":true`) {
				t.Errorf("first LB at %s not published with that sample", s.at.Format(time.TimeOnly))
			}
			if store.Rec == nil || !store.Rec.LowBatteryReached {
				t.Errorf("outage.json did not record the low battery at %s: %+v", s.at.Format(time.TimeOnly), store.Rec)
			}
		}
	}
	if firstLB < 0 {
		t.Fatal("the recording has no LB")
	}
	if got := varOf(snaps[firstLB].vars, "battery.voltage"); got != "10.8" {
		t.Errorf("first LB at battery.voltage %s, want 10.8 as recorded", got)
	}
	if l := lines(logs, "low battery:"); len(l) != 1 || !strings.Contains(l[0], "battery.voltage 10.8") {
		t.Errorf("want one low battery log line at 10.8 V, got %q", l)
	}
	st, ok := broker.Retained(emptyRunStateTopic)
	if !ok {
		t.Fatal("no state retained")
	}
	for _, name := range []string{`"battery.charge"`, `"battery.runtime"`} {
		if strings.Contains(st.Payload, name) {
			t.Errorf("state.variables carries the misdecoded %s:\n%s", name, st.Payload)
		}
	}
	started := snaps[0].at.Truncate(time.Second)

	// ── The host dies ─────────────────────────────────────────────────────
	// Mains returns and the Pi boots at 23:26:25, its clock restored to
	// 23:11:59 from the last time it saved; ups-mqtt starts 93 s later.
	realBoot := time.Date(2026, 11, 18, 23, 26, 25, 0, time.UTC)
	slow := 14*time.Minute + 26*time.Second
	bootB := -93 * time.Second
	clk2 := &outages.FakeClock{Wall: realBoot.Add(93*time.Second - slow), Boot: &bootB,
		IsSynced: false, SyncKnown: true, BootIDValue: "boot-b"}
	charging := withOverrides(snaps[len(snaps)-1].vars, map[string]string{
		"ups.status": "OL CHRG", "battery.voltage": "11.8", "input.voltage": "235",
	})
	d2 := &daemon{cfg: emptyRunCfg, poller: &nut.FakePoller{Variables: charging}, pub: broker,
		outage: outageState{tracker: outages.NewTracker(clk2, store)}}

	broker.Reset()
	// 15 s unsynced: the stale outage is cleared at once, but the summary
	// waits for the clock.
	for range 2 {
		d2.sample(clk2.Now(), false)
		clk2.Advance(10 * time.Second)
	}
	if broker.IndexOf(emptyRunOutageTopic) < 0 {
		t.Fatal("first poll on mains did not clear the outage")
	}
	// The path the Pi took, as its journal shows: the record is from an
	// earlier boot that saw LB, so the battery ran flat.  (On mains, the
	// fallback for an outage that ended unwatched would give the same
	// summary; this pins which branch the real data takes.)
	want := "outage started " + started.Format(time.RFC3339) + " ran the battery low and the host has rebooted since; it ended at boot"
	if l := lines(logs, want); len(l) != 1 {
		t.Errorf("want the journal line %q, got:\n%s", want, logs)
	}
	if _, ok := broker.Find(emptyRunLastTopic); ok {
		t.Fatal("last_outage published before the clock synced")
	}
	if l := lines(logs, "low battery cleared"); len(l) != 0 {
		// The new process never saw low battery; there is nothing to clear.
		t.Errorf("fresh process logged %q", l)
	}

	// NTP: the clock steps forward 14½ min.
	clk2.Step(slow)
	clk2.IsSynced = true
	d2.sample(clk2.Now(), false)

	if msg, ok := broker.Retained(emptyRunOutageTopic); ok {
		t.Errorf("outage still retained: %s", msg.Payload)
	}
	lo := decodeReplayLastOutage(t, broker)
	if lo.StartedAt != started.Format(time.RFC3339) || lo.EndedAt != realBoot.Format(time.RFC3339) {
		t.Errorf("last_outage %s → %s, want %s → the real boot %s", lo.StartedAt, lo.EndedAt, started.Format(time.RFC3339), realBoot.Format(time.RFC3339))
	}
	if !lo.LowBatteryReached || !lo.SpansRestart {
		t.Errorf("last_outage = %+v, want low_battery_reached and spans_restart", lo)
	}
	if lo.MinBatteryChargePct != nil {
		t.Errorf("min_battery_charge_pct = %v from a charge that cannot be real", *lo.MinBatteryChargePct)
	}
	if lo.ClockSynced == nil || !*lo.ClockSynced {
		t.Errorf("clock_synced = %v, want true", lo.ClockSynced)
	}
	if lo.Timestamp == "" {
		t.Error("last_outage has no timestamp")
	}
	if store.Rec != nil {
		t.Errorf("outage.json left after the summary: %+v", store.Rec)
	}
}

func decodeReplayLastOutage(t *testing.T, broker *publisher.FakePublisher) publisher.LastOutageMessage {
	t.Helper()
	msg, ok := broker.Retained(emptyRunLastTopic)
	if !ok {
		t.Fatal("no last_outage retained")
	}
	var lo publisher.LastOutageMessage
	if err := json.Unmarshal([]byte(msg.Payload), &lo); err != nil {
		t.Fatalf("decoding last_outage: %v", err)
	}
	return lo
}
