package outages

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 13, 10, 2, 10, 0, time.UTC)

func syncedClock() *FakeClock {
	return &FakeClock{Wall: t0, IsSynced: true, SyncKnown: true}
}

func pct(v float64) *float64 { return &v }

func mustDue(t *testing.T, tr *Tracker) Summary {
	t.Helper()
	s, ok := tr.Due()
	if !ok {
		t.Fatal("Due() = none, want a summary")
	}
	return s
}

func assertNotDue(t *testing.T, tr *Tracker) {
	t.Helper()
	if s, ok := tr.Due(); ok {
		t.Fatalf("Due() = %+v, want none", s)
	}
}

func assertSynced(t *testing.T, got *bool, want bool) {
	t.Helper()
	if got == nil {
		t.Fatalf("clock_synced = nil, want %v", want)
	}
	if *got != want {
		t.Errorf("clock_synced = %v, want %v", *got, want)
	}
}

// ── A normal outage, all in one process ────────────────────────────────────

func TestTracker_NormalOutage(t *testing.T) {
	clk, store := syncedClock(), &MemoryStore{}
	tr := NewTracker(clk, store)
	tr.Restore(false)

	o, isNew := tr.OnBattery(false, pct(100))
	if !isNew || !o.StartedAt.Equal(t0) {
		t.Fatalf("OnBattery = %+v, new %v; want a new outage at %s", o, isNew, t0)
	}
	assertSynced(t, o.ClockSynced, true)

	for _, c := range []float64{80, 61, 64} {
		clk.Advance(10 * time.Minute)
		if _, isNew := tr.OnBattery(false, pct(c)); isNew {
			t.Fatal("a continuing outage was reported as new")
		}
	}
	clk.Advance(7*time.Minute + 50*time.Second)
	assertNotDue(t, tr)

	if !tr.OnMains() {
		t.Fatal("OnMains() = false, want the outage to end")
	}
	if tr.OnMains() {
		t.Error("a second OnMains ended the outage again")
	}

	s := mustDue(t, tr)
	if !s.StartedAt.Equal(t0) || !s.EndedAt.Equal(t0.Add(37*time.Minute+50*time.Second)) {
		t.Errorf("summary %s → %s, want %s → +37m50s", s.StartedAt, s.EndedAt, t0)
	}
	if s.Duration() != 37*time.Minute+50*time.Second {
		t.Errorf("duration = %s", s.Duration())
	}
	if s.MinBatteryChargePct == nil || *s.MinBatteryChargePct != 61 {
		t.Errorf("min charge = %v, want 61", s.MinBatteryChargePct)
	}
	if s.LowBatteryReached || s.SpansRestart || !s.Final {
		t.Errorf("summary = %+v, want no LB, no restart, final", s)
	}
	assertSynced(t, s.ClockSynced, true)

	tr.Published(s)
	assertNotDue(t, tr)
	if store.Rec != nil {
		t.Errorf("state file left behind: %+v", store.Rec)
	}
}

// The file is written on transitions only: start, LB first seen, end.
// Charge changes and continuing polls are not, to spare the SD card.
func TestTracker_WritesOnlyOnTransitions(t *testing.T) {
	clk, store := syncedClock(), &MemoryStore{}
	tr := NewTracker(clk, store)
	tr.Restore(false)

	tr.OnBattery(false, pct(90))
	tr.OnBattery(false, pct(80))
	if store.Saves != 1 {
		t.Errorf("saves after start = %d, want 1", store.Saves)
	}
	tr.OnBattery(true, pct(20))
	tr.OnBattery(true, pct(15))
	if store.Saves != 2 || !store.Rec.LowBatteryReached {
		t.Errorf("saves after LB = %d, LB stored %v; want 2, true", store.Saves, store.Rec.LowBatteryReached)
	}
	tr.OnMains()
	tr.OnMains()
	if store.Saves != 3 || store.Rec.EndedAt == nil {
		t.Errorf("saves after end = %d, ended stored %v; want 3 with ended_at", store.Saves, store.Rec.EndedAt)
	}
}

func TestTracker_LowBatteryReached(t *testing.T) {
	tr := NewTracker(syncedClock(), &MemoryStore{})
	tr.Restore(false)
	tr.OnBattery(false, nil)
	tr.OnBattery(true, nil)
	tr.OnBattery(false, nil) // LB clearing later does not unset it
	tr.OnMains()
	if s := mustDue(t, tr); !s.LowBatteryReached {
		t.Error("low_battery_reached = false after LB was seen")
	}
}

// A UPS that reports no battery.charge (the Riello iPlug) has no minimum.
func TestTracker_NoChargeReported_MinAbsent(t *testing.T) {
	tr := NewTracker(syncedClock(), &MemoryStore{})
	tr.Restore(false)
	tr.OnBattery(false, nil)
	tr.OnMains()
	if s := mustDue(t, tr); s.MinBatteryChargePct != nil {
		t.Errorf("min charge = %v, want absent", *s.MinBatteryChargePct)
	}
}

// A failed publish is retried: Due keeps returning the summary until
// Published, and the state file stays.
func TestTracker_UnpublishedSummaryIsRetried(t *testing.T) {
	clk, store := syncedClock(), &MemoryStore{}
	tr := NewTracker(clk, store)
	tr.Restore(false)
	tr.OnBattery(false, nil)
	clk.Advance(time.Minute)
	tr.OnMains()

	first := mustDue(t, tr)
	clk.Advance(10 * time.Second) // next poll
	again := mustDue(t, tr)
	if !again.EndedAt.Equal(first.EndedAt) {
		t.Errorf("retried summary ended %s, first said %s", again.EndedAt, first.EndedAt)
	}
	if store.Rec == nil {
		t.Error("state file removed before the summary was published")
	}
}

// ── Restart mid-outage ─────────────────────────────────────────────────────

// The case that matters most: the battery ran flat, the host died, and mains
// came back while nothing was running.  The previous process's record says
// the outage is open; this one starts on mains.
func TestTracker_RestartOnMains_SpansRestartEndsAtBoot(t *testing.T) {
	store := &MemoryStore{}
	prev := NewTracker(syncedClock(), store)
	prev.Restore(false)
	prev.OnBattery(false, pct(90))
	prev.OnBattery(true, pct(5)) // LB, then the host dies

	// Mains returned at t0+3h, booting the host; this process starts 40 s
	// after boot.
	boot := -40 * time.Second
	clk := &FakeClock{Wall: t0.Add(3*time.Hour + 40*time.Second), Boot: &boot, IsSynced: true, SyncKnown: true}
	tr := NewTracker(clk, store)
	tr.Restore(false)
	if tr.OnMains() {
		t.Error("OnMains reported an outage ending in this process")
	}

	s := mustDue(t, tr)
	if !s.StartedAt.Equal(t0) || !s.EndedAt.Equal(t0.Add(3*time.Hour)) {
		t.Errorf("summary %s → %s, want %s → boot at +3h", s.StartedAt, s.EndedAt, t0)
	}
	if !s.SpansRestart || !s.LowBatteryReached || !s.Final {
		t.Errorf("summary = %+v, want spans_restart, LB, final", s)
	}
	if s.MinBatteryChargePct != nil {
		t.Errorf("min charge = %v across a restart, want absent", *s.MinBatteryChargePct)
	}
	assertSynced(t, s.ClockSynced, true)

	tr.Published(s)
	if store.Rec != nil {
		t.Error("state file not removed after the summary was published")
	}
}

// Only the process restarted (a deploy mid-outage); the host booted long
// before the outage, so boot time is no estimate.  The end is taken as this
// process's start.
func TestTracker_RestartOnMains_HostUp_EndsAtProcessStart(t *testing.T) {
	store := &MemoryStore{}
	prev := NewTracker(syncedClock(), store)
	prev.Restore(false)
	prev.OnBattery(false, nil)

	boot := -30 * 24 * time.Hour
	clk := &FakeClock{Wall: t0.Add(5 * time.Minute), Boot: &boot, IsSynced: true, SyncKnown: true}
	tr := NewTracker(clk, store)
	tr.Restore(false)
	clk.Advance(10 * time.Second) // first poll a little after start

	s := mustDue(t, tr)
	if !s.EndedAt.Equal(t0.Add(5 * time.Minute)) {
		t.Errorf("ended_at = %s, want process start %s", s.EndedAt, t0.Add(5*time.Minute))
	}
}

// Without a boot time (non-Linux), the end is the process start.
func TestTracker_RestartOnMains_NoBootTime(t *testing.T) {
	store := &MemoryStore{}
	prev := NewTracker(syncedClock(), store)
	prev.Restore(false)
	prev.OnBattery(false, nil)

	clk := &FakeClock{Wall: t0.Add(2 * time.Hour), IsSynced: true, SyncKnown: true}
	tr := NewTracker(clk, store)
	tr.Restore(false)
	if s := mustDue(t, tr); !s.EndedAt.Equal(t0.Add(2 * time.Hour)) {
		t.Errorf("ended_at = %s, want process start", s.EndedAt)
	}
}

// Still on battery after the restart: resume, keeping the original start.
func TestTracker_RestartOnBattery_Resumes(t *testing.T) {
	store := &MemoryStore{}
	prev := NewTracker(syncedClock(), store)
	prev.Restore(false)
	prev.OnBattery(false, pct(70))

	clk := &FakeClock{Wall: t0.Add(20 * time.Minute), IsSynced: true, SyncKnown: true}
	tr := NewTracker(clk, store)
	tr.Restore(true)
	o, isNew := tr.OnBattery(false, pct(40))
	if isNew {
		t.Error("resumed outage reported as new")
	}
	if !o.StartedAt.Equal(t0) {
		t.Errorf("outage start = %s, want the original %s", o.StartedAt, t0)
	}
	clk.Advance(5 * time.Minute)
	tr.OnMains()

	s := mustDue(t, tr)
	if !s.StartedAt.Equal(t0) || !s.EndedAt.Equal(t0.Add(25*time.Minute)) {
		t.Errorf("summary %s → %s, want %s → +25m", s.StartedAt, s.EndedAt, t0)
	}
	if !s.SpansRestart {
		t.Error("spans_restart = false for a resumed outage")
	}
	if s.MinBatteryChargePct != nil {
		t.Error("min charge reported for an outage partly seen by another process")
	}
}

// An outage that ended in the previous process, which died before
// publishing its summary, is reported as stored.
func TestTracker_EndedRecordOnDisk_PublishedAsStored(t *testing.T) {
	store := &MemoryStore{}
	prevClk := syncedClock()
	prev := NewTracker(prevClk, store)
	prev.Restore(false)
	prev.OnBattery(false, nil)
	prevClk.Advance(4 * time.Minute)
	prev.OnMains() // ...and dies before publishing

	tr := NewTracker(&FakeClock{Wall: t0.Add(time.Hour), IsSynced: true, SyncKnown: true}, store)
	tr.Restore(true) // even if on battery again now
	s := mustDue(t, tr)
	if !s.StartedAt.Equal(t0) || !s.EndedAt.Equal(t0.Add(4*time.Minute)) || !s.Final {
		t.Errorf("summary = %+v, want the stored %s → +4m, final", s, t0)
	}
	if s.SpansRestart {
		t.Error("spans_restart = true for an outage that ended within one process")
	}
	assertSynced(t, s.ClockSynced, true)
}

func TestTracker_CorruptStateDiscarded(t *testing.T) {
	store := &errStore{loadErr: fmt.Errorf("%w: unexpected end of JSON input", ErrCorrupt)}
	tr := NewTracker(syncedClock(), store)
	tr.Restore(false)
	assertNotDue(t, tr)
	if !store.removed {
		t.Error("corrupt state file was not removed")
	}
}

// An I/O error may be transient (a failing SD card): the file must be left
// for a later process, not destroyed along with a real outage.
func TestTracker_UnreadableStateKept(t *testing.T) {
	store := &errStore{loadErr: errors.New("read outage.json: input/output error")}
	tr := NewTracker(syncedClock(), store)
	tr.Restore(false)
	if store.removed {
		t.Error("state file removed on an I/O error")
	}
}

// Restore acts once: a later poll on mains must not re-read the file the
// current outage has since written.
func TestTracker_RestoreOnlyOnce(t *testing.T) {
	store := &MemoryStore{}
	tr := NewTracker(syncedClock(), store)
	tr.Restore(false)
	tr.OnBattery(false, nil)
	tr.Restore(false)
	if s, ok := tr.Due(); ok {
		t.Errorf("a second Restore turned the open outage into a summary: %+v", s)
	}
}

// ── The clock ──────────────────────────────────────────────────────────────

// Unsynced at the start, synced by the end: both times come out right,
// because the start is anchored on the monotonic clock.
func TestTracker_ClockSyncsMidOutage_StartRecomputed(t *testing.T) {
	// The clock reads two hours slow until NTP syncs.
	clk := &FakeClock{Wall: t0.Add(-2 * time.Hour), SyncKnown: true}
	tr := NewTracker(clk, &MemoryStore{})
	tr.Restore(false)
	o, _ := tr.OnBattery(false, nil)
	assertSynced(t, o.ClockSynced, false)

	clk.Advance(3 * time.Minute)
	clk.Step(2 * time.Hour) // NTP
	clk.Advance(7 * time.Minute)
	o, _ = tr.OnBattery(false, nil)
	if !o.StartedAt.Equal(t0) {
		t.Errorf("outage message start = %s after sync, want corrected %s", o.StartedAt, t0)
	}
	assertSynced(t, o.ClockSynced, true)
	tr.OnMains()

	s := mustDue(t, tr)
	if !s.StartedAt.Equal(t0) || s.Duration() != 10*time.Minute {
		t.Errorf("summary %s, %s; want start %s, 10m", s.StartedAt, s.Duration(), t0)
	}
	assertSynced(t, s.ClockSynced, true)
}

// After a power-loss reboot with the uplink still down, the clock does not
// sync.  The summary waits SyncWait, goes out as interim, then is corrected
// once the clock syncs — and the state file stays until then.
func TestTracker_RestartUnsynced_InterimThenCorrected(t *testing.T) {
	store := &MemoryStore{}
	prev := NewTracker(syncedClock(), store)
	prev.Restore(false)
	prev.OnBattery(false, nil)

	// Real boot at t0+3h.  The restored clock reads t0+1h (timesyncd's saved
	// time, earlier than the truth) and is unsynced.
	boot := -30 * time.Second
	clk := &FakeClock{Wall: t0.Add(time.Hour + 30*time.Second), Boot: &boot, SyncKnown: true}
	tr := NewTracker(clk, store)
	tr.Restore(false)

	assertNotDue(t, tr)
	clk.Advance(DefaultSyncWait - time.Second)
	assertNotDue(t, tr)
	clk.Advance(time.Second)

	interim := mustDue(t, tr)
	if interim.Final {
		t.Error("summary before the clock synced is final, want interim")
	}
	assertSynced(t, interim.ClockSynced, false)
	if !interim.EndedAt.Equal(t0.Add(time.Hour)) {
		t.Errorf("interim ended_at = %s, want the unsynced boot reading %s", interim.EndedAt, t0.Add(time.Hour))
	}
	tr.Published(interim)
	if store.Rec == nil {
		t.Fatal("state file removed after an interim summary")
	}
	clk.Advance(time.Hour)
	assertNotDue(t, tr) // no second interim

	clk.Step(2 * time.Hour) // NTP, at last
	final := mustDue(t, tr)
	if !final.Final || !final.EndedAt.Equal(t0.Add(3*time.Hour)) {
		t.Errorf("corrected summary = %+v, want final, ended at the true boot %s", final, t0.Add(3*time.Hour))
	}
	assertSynced(t, final.ClockSynced, true)
	tr.Published(final)
	if store.Rec != nil {
		t.Error("state file not removed after the corrected summary")
	}
}

// A start recorded while unsynced, by an earlier boot, cannot be repaired.
func TestTracker_PersistedUnsyncedStart_StaysUnsynced(t *testing.T) {
	store := &MemoryStore{}
	prev := NewTracker(&FakeClock{Wall: t0, SyncKnown: true}, store) // unsynced
	prev.Restore(false)
	prev.OnBattery(false, nil)

	boot := -time.Minute
	tr := NewTracker(&FakeClock{Wall: t0.Add(time.Hour), Boot: &boot, IsSynced: true, SyncKnown: true}, store)
	tr.Restore(false)
	s := mustDue(t, tr)
	if !s.Final {
		t.Error("summary not final once synced")
	}
	assertSynced(t, s.ClockSynced, false)
}

// Where clock sync cannot be determined (macOS), summaries are final at
// once and clock_synced is absent.
func TestTracker_SyncUnknown_FinalAndAbsent(t *testing.T) {
	clk := &FakeClock{Wall: t0}
	tr := NewTracker(clk, &MemoryStore{})
	tr.Restore(false)
	o, _ := tr.OnBattery(false, nil)
	if o.ClockSynced != nil {
		t.Errorf("outage clock_synced = %v, want absent", *o.ClockSynced)
	}
	clk.Advance(time.Minute)
	tr.OnMains()
	s := mustDue(t, tr)
	if !s.Final || s.ClockSynced != nil {
		t.Errorf("summary = %+v, want final with clock_synced absent", s)
	}
}

// ── Overlapping outages ────────────────────────────────────────────────────

// Power fails again while a summary is still waiting for the clock.  The
// summary goes out final at once, and publishing it must not delete the new
// outage's state.
func TestTracker_NewOutageWhileSummaryPending(t *testing.T) {
	clk, store := &FakeClock{Wall: t0, SyncKnown: true}, &MemoryStore{}
	tr := NewTracker(clk, store)
	tr.Restore(false)
	tr.OnBattery(false, nil)
	clk.Advance(time.Minute)
	tr.OnMains()
	assertNotDue(t, tr) // waiting for a sync

	clk.Advance(time.Minute)
	if _, isNew := tr.OnBattery(false, nil); !isNew {
		t.Fatal("second outage not reported as new")
	}
	s := mustDue(t, tr)
	if !s.Final || s.Duration() != time.Minute {
		t.Errorf("pending summary = %+v, want final, 1m", s)
	}
	tr.Published(s)
	if store.Rec == nil || store.Rec.EndedAt != nil {
		t.Errorf("state = %+v, want the second outage's open record", store.Rec)
	}
}

// A store that cannot be written must not stop tracking.
func TestTracker_SaveErrorIsNotFatal(t *testing.T) {
	clk := syncedClock()
	tr := NewTracker(clk, &MemoryStore{SaveErr: errors.New("read-only file system")})
	tr.Restore(false)
	tr.OnBattery(false, nil)
	clk.Advance(time.Minute)
	tr.OnMains()
	if s := mustDue(t, tr); s.Duration() != time.Minute {
		t.Errorf("duration = %s, want 1m", s.Duration())
	}
}

func TestTracker_PublishedWithNothingPending(t *testing.T) {
	tr := NewTracker(syncedClock(), &MemoryStore{})
	tr.Published(Summary{Final: true}) // must not panic
}

// errStore fails Load, recording whether Remove was called.
type errStore struct {
	loadErr error
	removed bool
}

func (s *errStore) Load() (*Record, error) { return nil, s.loadErr }
func (s *errStore) Save(Record) error      { return nil }
func (s *errStore) Remove() error          { s.removed = true; return nil }

// ── Reboots: the boot ID ───────────────────────────────────────────────────

// bootClock is a synced clock in boot id, whose host booted bootAgo before
// this process started.
func bootClock(id string, wall time.Time, bootAgo time.Duration) *FakeClock {
	boot := -bootAgo
	return &FakeClock{Wall: wall, Boot: &boot, IsSynced: true, SyncKnown: true, BootIDValue: id}
}

// Outage A runs the battery flat; the host dies.  Mains returns and boots
// it, then fails again before this process's first poll.  A ended at boot;
// this is outage B, not A resumed with hours of someone else's duration.
func TestTracker_RebootThenNewOutageBeforeFirstPoll(t *testing.T) {
	store := &MemoryStore{}
	prev := NewTracker(bootClock("boot-a", t0, time.Hour), store)
	prev.Restore(false)
	prev.OnBattery(false, nil)
	prev.OnBattery(true, nil) // LB, then the host dies

	bootedAt := t0.Add(6 * time.Hour)
	clk := bootClock("boot-b", bootedAt.Add(time.Minute), time.Minute)
	tr := NewTracker(clk, store)
	tr.Restore(true)
	b, isNew := tr.OnBattery(false, nil)
	if !isNew || !b.StartedAt.Equal(bootedAt.Add(time.Minute)) {
		t.Fatalf("OnBattery = %+v, new %v; want a new outage starting now, not A resumed", b, isNew)
	}

	a := mustDue(t, tr)
	if !a.StartedAt.Equal(t0) || !a.EndedAt.Equal(bootedAt) || !a.SpansRestart || !a.LowBatteryReached || !a.Final {
		t.Errorf("A's summary = %+v, want %s → boot %s, spans_restart, LB, final", a, t0, bootedAt)
	}
	tr.Published(a)
	if store.Rec == nil || !store.Rec.StartedAt.Equal(bootedAt.Add(time.Minute)) {
		t.Errorf("state = %+v, want B's open record kept", store.Rec)
	}

	clk.Advance(time.Minute)
	tr.OnMains()
	bs := mustDue(t, tr)
	if bs.Duration() != time.Minute || bs.SpansRestart {
		t.Errorf("B's summary = %+v, want 1m within one process", bs)
	}
}

// A reboot without low battery is ambiguous — the host may have rebooted on
// UPS power — so an outage still in progress is resumed.
func TestTracker_RebootWithoutLowBattery_Resumes(t *testing.T) {
	store := &MemoryStore{}
	prev := NewTracker(bootClock("boot-a", t0, time.Hour), store)
	prev.Restore(false)
	prev.OnBattery(false, nil)

	tr := NewTracker(bootClock("boot-b", t0.Add(10*time.Minute), time.Minute), store)
	tr.Restore(true)
	if o, isNew := tr.OnBattery(false, nil); isNew || !o.StartedAt.Equal(t0) {
		t.Errorf("OnBattery = %+v, new %v; want the outage resumed from %s", o, isNew, t0)
	}
}

// A process-only restart (a deploy) mid-outage, while the clock is still
// unsynced: the start is re-anchored from time-since-boot, so a later NTP
// sync corrects it rather than leaving it frozen at the wrong wall time.
func TestTracker_ProcessRestart_StartStaysRepairable(t *testing.T) {
	store := &MemoryStore{}
	// The clock reads 2h slow and unsynced.  Host booted 10 min ago.
	boot := -10 * time.Minute
	prevClk := &FakeClock{Wall: t0.Add(-2 * time.Hour), Boot: &boot, SyncKnown: true, BootIDValue: "boot-a"}
	prev := NewTracker(prevClk, store)
	prev.Restore(false)
	prev.OnBattery(false, nil)

	// The new process starts 5 min later, same boot, clock still slow.
	clk := bootClock("boot-a", t0.Add(-2*time.Hour+5*time.Minute), 15*time.Minute)
	clk.IsSynced = false
	tr := NewTracker(clk, store)
	tr.Restore(true)
	tr.OnBattery(false, nil)

	clk.Step(2 * time.Hour) // NTP
	o, _ := tr.Current()
	if !o.StartedAt.Equal(t0) {
		t.Errorf("start after sync = %s, want corrected %s", o.StartedAt, t0)
	}
	assertSynced(t, o.ClockSynced, true)
	clk.Advance(time.Minute)
	tr.OnMains()
	s := mustDue(t, tr)
	if !s.StartedAt.Equal(t0) || s.Duration() != 6*time.Minute {
		t.Errorf("summary %s lasting %s, want %s lasting 6m", s.StartedAt, s.Duration(), t0)
	}
	assertSynced(t, s.ClockSynced, true)
}

// A process-only restart on mains: the outage ended before this process
// started, not at boot — even when the wall clocks would suggest otherwise.
func TestTracker_ProcessRestartOnMains_EndsAtProcessStart(t *testing.T) {
	store := &MemoryStore{}
	prev := NewTracker(bootClock("boot-a", t0, 10*time.Minute), store)
	prev.Restore(false)
	prev.OnBattery(false, nil)

	// Same boot; the process starts 5 min later.  The record's wall start
	// is skewed late, so comparing walls alone would pick boot time.
	store.Rec.StartedAt = t0.Add(-20 * time.Minute)
	store.Rec.StartSinceBoot = nil
	tr := NewTracker(bootClock("boot-a", t0.Add(5*time.Minute), 15*time.Minute), store)
	tr.Restore(false)
	if s := mustDue(t, tr); !s.EndedAt.Equal(t0.Add(5 * time.Minute)) {
		t.Errorf("ended_at = %s, want process start %s", s.EndedAt, t0.Add(5*time.Minute))
	}
}

// last_outage is the most recent outage: a summary still unpublished when
// the next outage ends is superseded (and logged), not queued.
func TestTracker_UnpublishedSummarySupersededByNextOutage(t *testing.T) {
	clk := syncedClock()
	tr := NewTracker(clk, &MemoryStore{})
	tr.Restore(false)
	tr.OnBattery(false, nil)
	clk.Advance(time.Minute)
	tr.OnMains() // A ends; its publishes keep failing
	mustDue(t, tr)

	clk.Advance(time.Minute)
	tr.OnBattery(false, nil) // B
	clk.Advance(3 * time.Minute)
	tr.OnMains()
	s := mustDue(t, tr)
	if s.Duration() != 3*time.Minute {
		t.Errorf("summary lasts %s, want B's 3m", s.Duration())
	}
	tr.Published(s)
	assertNotDue(t, tr)
}

// The outage ends while the clock is unsynced; the process restarts within
// the same boot before the summary is published; then NTP steps the clock.
// Both ends are re-anchored, so the summary waits for sync and comes out
// right — not a 0 s outage from a start on the new clock and an end on the
// old one.
func TestTracker_ProcessRestartAfterEnd_UnsyncedThenStep(t *testing.T) {
	store := &MemoryStore{}
	// The clock reads 30 days slow and unsynced; host booted 1h ago.
	boot := -time.Hour
	prevClk := &FakeClock{Wall: t0.Add(-30 * 24 * time.Hour), Boot: &boot, SyncKnown: true, BootIDValue: "boot-a"}
	prev := NewTracker(prevClk, store)
	prev.Restore(false)
	prev.OnBattery(false, nil)
	prevClk.Advance(20 * time.Minute)
	prev.OnMains() // ...waits for sync; then a deploy restarts the process

	clk := bootClock("boot-a", prevClk.Wall.Add(time.Minute), time.Hour+21*time.Minute)
	clk.IsSynced = false
	tr := NewTracker(clk, store)
	tr.Restore(false)
	assertNotDue(t, tr) // still repairable, so it waits

	clk.Step(30 * 24 * time.Hour) // NTP
	s := mustDue(t, tr)
	if !s.StartedAt.Equal(t0) || s.Duration() != 20*time.Minute {
		t.Errorf("summary %s lasting %s, want %s lasting 20m", s.StartedAt, s.Duration(), t0)
	}
	assertSynced(t, s.ClockSynced, true)
	if !s.Final {
		t.Error("summary not final once synced")
	}
}

// An ended record written before end_since_boot existed: neither end is
// re-anchored, so start and end stay consistent with each other.
func TestTracker_EndedRecordWithoutEndAnchor_KeepsStoredTimes(t *testing.T) {
	since := 10 * time.Minute
	end := t0.Add(20 * time.Minute)
	store := &MemoryStore{Rec: &Record{StartedAt: t0, BootID: "boot-a", StartSinceBoot: &since, EndedAt: &end}}

	clk := bootClock("boot-a", t0.Add(time.Hour), 2*time.Hour)
	tr := NewTracker(clk, store)
	tr.Restore(false)
	clk.Step(24 * time.Hour)
	s := mustDue(t, tr)
	if !s.StartedAt.Equal(t0) || s.Duration() != 20*time.Minute {
		t.Errorf("summary %s lasting %s, want the stored %s lasting 20m", s.StartedAt, s.Duration(), t0)
	}
}

// A start carried over from an earlier boot has no anchor in this one, so
// the ended record must not keep the old boot's start_since_boot under the
// new boot ID.
func TestTracker_EndAfterResumeAcrossReboot_DropsStaleStartAnchor(t *testing.T) {
	store := &MemoryStore{}
	prev := NewTracker(bootClock("boot-a", t0, time.Hour), store)
	prev.Restore(false)
	prev.OnBattery(false, nil) // no LB: resumed across the reboot

	clk := bootClock("boot-b", t0.Add(10*time.Minute), time.Minute)
	tr := NewTracker(clk, store)
	tr.Restore(true)
	tr.OnBattery(false, nil)
	clk.Advance(time.Minute)
	tr.OnMains()

	if store.Rec.BootID != "boot-b" || store.Rec.StartSinceBoot != nil || store.Rec.EndSinceBoot == nil {
		t.Errorf("ended record = boot %q, start anchor %v, end anchor %v; want boot-b, no start anchor, an end anchor",
			store.Rec.BootID, store.Rec.StartSinceBoot, store.Rec.EndSinceBoot)
	}
}

// Resumed across a reboot on UPS power, then low battery in the new boot,
// then a process-only restart while still on battery.  That is one outage,
// still in progress: it must be resumed, not ended at the reboot with a
// false last_outage published while the power is still off.
func TestTracker_LBAfterResumeAcrossReboot_ProcessRestartResumes(t *testing.T) {
	store := &MemoryStore{}
	prev := NewTracker(bootClock("boot-a", t0, time.Hour), store)
	prev.Restore(false)
	prev.OnBattery(false, nil)

	// The host reboots on UPS power at t0+10m; no LB yet, so resume.
	mid := NewTracker(bootClock("boot-b", t0.Add(11*time.Minute), time.Minute), store)
	mid.Restore(true)
	mid.OnBattery(false, nil)
	if store.Rec.BootID != "boot-b" || store.Rec.StartSinceBoot != nil {
		t.Errorf("resumed record = boot %q, start anchor %v; want re-stamped for boot-b with no start anchor",
			store.Rec.BootID, store.Rec.StartSinceBoot)
	}
	mid.OnBattery(true, nil) // LB, in boot-b

	// A deploy restarts the process, still in boot-b, still on battery.
	tr := NewTracker(bootClock("boot-b", t0.Add(21*time.Minute), 11*time.Minute), store)
	tr.Restore(true)
	o, isNew := tr.OnBattery(true, nil)
	if isNew || !o.StartedAt.Equal(t0) {
		t.Errorf("OnBattery = start %s, new %v; want the outage from %s resumed", o.StartedAt, isNew, t0)
	}
	assertNotDue(t, tr)
}
