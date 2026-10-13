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
	rielloOutageTopic     = "ups/riello-ups/outage"
	rielloLastOutageTopic = "ups/riello-ups/last_outage"
	rielloStateTopic      = "ups/riello-ups/state"
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

// ── last_outage across a restart (#5) ──────────────────────────────────────
//
// The broker and the state file both outlive the process; each process gets
// its own clock, as a reboot resets the monotonic one.

// process returns the outage state of a freshly started daemon whose clock
// reads wall and whose host booted bootAgo before it started.
func process(store *outages.MemoryStore, wall time.Time, bootAgo time.Duration, synced bool) (*outageState, *outages.FakeClock) {
	boot := -bootAgo
	clk := &outages.FakeClock{Wall: wall, Boot: &boot, IsSynced: synced, SyncKnown: true}
	return &outageState{tracker: outages.NewTracker(clk, store)}, clk
}

func decodeLastOutage(t *testing.T, broker *publisher.FakePublisher) publisher.LastOutageMessage {
	t.Helper()
	msg, ok := broker.Retained(rielloLastOutageTopic)
	if !ok {
		t.Fatal("no last_outage retained")
	}
	var lo publisher.LastOutageMessage
	if err := json.Unmarshal([]byte(msg.Payload), &lo); err != nil {
		t.Fatalf("decoding last_outage: %v", err)
	}
	return lo
}

// The battery runs flat, the host dies, mains returns and boots it.  The new
// daemon clears the stale outage and then reports the outage it never saw
// end, ending at boot.
func TestRestartAfterOutage_PublishesLastOutageSpanningRestart(t *testing.T) {
	online := loadFixture(t, "listvar_riello.txt")
	onBattery := withOverrides(online, map[string]string{"ups.status": "OB"})
	lowBattery := withOverrides(online, map[string]string{"ups.status": "OB LB"})
	broker := &publisher.FakePublisher{}
	store := &outages.MemoryStore{}

	start := time.Now().Add(-3 * time.Hour).Truncate(time.Second)
	first, clk := process(store, start, time.Hour, true)
	for _, vars := range [][]nut.Variable{onBattery, onBattery, lowBattery} {
		if err := doPoll(&nut.FakePoller{Variables: vars}, broker, rielloCfg, first); err != nil {
			t.Fatalf("process 1: %v", err)
		}
		clk.Advance(10 * time.Second)
	}
	// The host dies.  Mains returns 2h50m later; the host boots and the
	// daemon starts 35 s after boot.
	bootedAt := start.Add(2*time.Hour + 50*time.Minute)
	second, _ := process(store, bootedAt.Add(35*time.Second), 35*time.Second, true)
	if err := doPoll(&nut.FakePoller{Variables: online}, broker, rielloCfg, second); err != nil {
		t.Fatalf("process 2: %v", err)
	}

	if msg, ok := broker.Retained(rielloOutageTopic); ok {
		t.Errorf("stale outage still retained: %s", msg.Payload)
	}
	n := len(broker.Messages)
	if c, l := broker.IndexOf(rielloOutageTopic), lastIndexOf(broker, rielloLastOutageTopic); l < 0 || l < lastIndexOf(broker, rielloOutageTopic) {
		t.Errorf("clear at %d, last_outage at %d of %d; want last_outage after the clear", c, l, n)
	}

	lo := decodeLastOutage(t, broker)
	if lo.StartedAt != start.UTC().Format(time.RFC3339) || lo.EndedAt != bootedAt.UTC().Format(time.RFC3339) {
		t.Errorf("last_outage %s → %s, want %s → boot at %s", lo.StartedAt, lo.EndedAt, start.UTC().Format(time.RFC3339), bootedAt.UTC().Format(time.RFC3339))
	}
	if lo.DurationSecs != int64((2*time.Hour + 50*time.Minute).Seconds()) {
		t.Errorf("duration_secs = %d", lo.DurationSecs)
	}
	if !lo.SpansRestart || !lo.LowBatteryReached {
		t.Errorf("last_outage = %+v, want spans_restart and low_battery_reached", lo)
	}
	if lo.MinBatteryChargePct != nil {
		t.Errorf("min_battery_charge_pct = %v; the iPlug reports no charge", *lo.MinBatteryChargePct)
	}
	if lo.ClockSynced == nil || !*lo.ClockSynced {
		t.Errorf("clock_synced = %v, want true", lo.ClockSynced)
	}
	if store.Rec != nil {
		t.Errorf("state file left after the summary was published: %+v", store.Rec)
	}

	// Further polls publish nothing more about it.
	broker.Reset()
	if err := doPoll(&nut.FakePoller{Variables: online}, broker, rielloCfg, second); err != nil {
		t.Fatal(err)
	}
	if _, ok := broker.Find(rielloLastOutageTopic); ok {
		t.Error("last_outage published again")
	}
}

// Restart while still on battery: the outage message keeps the original
// start, and outage_duration_secs keeps counting from it.
func TestRestartDuringOutage_ResumesOriginalStart(t *testing.T) {
	online := loadFixture(t, "listvar_riello.txt")
	onBattery := withOverrides(online, map[string]string{"ups.status": "OB"})
	broker := &publisher.FakePublisher{}
	store := &outages.MemoryStore{}

	start := time.Now().Add(-10 * time.Minute).Truncate(time.Second)
	first, _ := process(store, start, time.Hour, true)
	if err := doPoll(&nut.FakePoller{Variables: onBattery}, broker, rielloCfg, first); err != nil {
		t.Fatal(err)
	}
	second, clk := process(store, time.Now(), time.Hour, true)
	if err := doPoll(&nut.FakePoller{Variables: onBattery}, broker, rielloCfg, second); err != nil {
		t.Fatal(err)
	}

	msg, _ := broker.Retained(rielloOutageTopic)
	var om publisher.OutageMessage
	if err := json.Unmarshal([]byte(msg.Payload), &om); err != nil {
		t.Fatal(err)
	}
	if om.OutageStartedAt != start.UTC().Format(time.RFC3339) {
		t.Errorf("outage_started_at = %s after restart, want the original %s", om.OutageStartedAt, start.UTC().Format(time.RFC3339))
	}
	if om.OutageDurationSecs < 599 {
		t.Errorf("outage_duration_secs = %d, want ~600 counted from the original start", om.OutageDurationSecs)
	}

	clk.Advance(time.Minute)
	if err := doPoll(&nut.FakePoller{Variables: online}, broker, rielloCfg, second); err != nil {
		t.Fatal(err)
	}
	if lo := decodeLastOutage(t, broker); !lo.SpansRestart || lo.StartedAt != start.UTC().Format(time.RFC3339) {
		t.Errorf("last_outage = %+v, want spans_restart from the original start", lo)
	}
}

// After a power-loss reboot the clock is unsynced and the uplink still down.
// An interim last_outage goes out after the sync wait; once NTP syncs, the
// corrected one replaces it on the broker.
func TestRestartUnsynced_InterimThenCorrectedLastOutage(t *testing.T) {
	online := loadFixture(t, "listvar_riello.txt")
	onBattery := withOverrides(online, map[string]string{"ups.status": "OB"})
	broker := &publisher.FakePublisher{}
	store := &outages.MemoryStore{}

	start := time.Now().Add(-4 * time.Hour).Truncate(time.Second)
	first, _ := process(store, start, time.Hour, true)
	if err := doPoll(&nut.FakePoller{Variables: onBattery}, broker, rielloCfg, first); err != nil {
		t.Fatal(err)
	}

	// Real boot at start+3h; the restored clock reads an hour slow.
	bootedAt := start.Add(3 * time.Hour)
	second, clk := process(store, bootedAt.Add(-time.Hour+20*time.Second), 20*time.Second, false)
	poll := func() {
		t.Helper()
		if err := doPoll(&nut.FakePoller{Variables: online}, broker, rielloCfg, second); err != nil {
			t.Fatal(err)
		}
	}
	poll()
	if _, ok := broker.Retained(rielloLastOutageTopic); ok {
		t.Fatal("last_outage published before the clock synced or the wait ran out")
	}

	clk.Advance(outages.DefaultSyncWait)
	poll()
	interim := decodeLastOutage(t, broker)
	if interim.ClockSynced == nil || *interim.ClockSynced {
		t.Errorf("interim clock_synced = %v, want false", interim.ClockSynced)
	}
	if store.Rec == nil {
		t.Fatal("state file removed after the interim summary")
	}

	clk.Step(time.Hour) // NTP
	poll()
	final := decodeLastOutage(t, broker)
	if final.EndedAt != bootedAt.UTC().Format(time.RFC3339) || final.ClockSynced == nil || !*final.ClockSynced {
		t.Errorf("corrected last_outage = %+v, want ended at the true boot %s, clock_synced", final, bootedAt.UTC().Format(time.RFC3339))
	}
	if store.Rec != nil {
		t.Error("state file left after the corrected summary")
	}
}

func lastIndexOf(f *publisher.FakePublisher, topic string) int {
	for i := len(f.Messages) - 1; i >= 0; i-- {
		if f.Messages[i].Topic == topic {
			return i
		}
	}
	return -1
}
