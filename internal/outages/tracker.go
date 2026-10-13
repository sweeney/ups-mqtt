// Package outages follows each power outage from start to end, across
// process restarts and power loss, and reports it once it is over.
//
// The Tracker answers two questions the poll loop cannot answer alone: when
// did the current outage really start (a previous process may have seen it
// begin), and what should be said about an outage that has ended — including
// one that ended while no process was running, because the battery ran flat
// and took the host down with it.
//
// Two clocks are involved.  The wall clock is what gets published, but on a
// host without an RTC it is wrong after a power-loss reboot until NTP syncs.
// The monotonic clock never steps.  Every event in this process is anchored
// on the monotonic clock, and its wall time is worked out only when a summary
// is published: "now, minus how long ago it happened".  Once the clock has
// synced, that answer is right however wrong the clock was at the time.
package outages

import (
	"errors"
	"log"
	"time"
)

// DefaultSyncWait is how long a summary waits for the wall clock to sync
// before an interim one is published anyway, marked clock_synced: false.
// The corrected summary follows once the clock syncs.
const DefaultSyncWait = 15 * time.Minute

// Clock is the time source.  SystemClock is the real one; FakeClock is for
// tests.
type Clock interface {
	// Now is the wall clock.
	Now() time.Time
	// Mono is a monotonic reading that never steps.  Its zero is the start
	// of this process.
	Mono() time.Duration
	// BootMono is the Mono reading at host boot (negative), when known.
	BootMono() (time.Duration, bool)
	// Synced reports whether the wall clock is synchronised to a time
	// source.  known is false where that cannot be determined.
	Synced() (synced, known bool)
	// BootID identifies this boot of the host; it changes on every reboot.
	// Empty where it cannot be determined.
	BootID() string
}

// Record is what is persisted about an outage.  It is written only on
// transitions — start, low battery first seen, end — to spare the SD card.
type Record struct {
	StartedAt         time.Time `json:"started_at"`
	StartClockSynced  *bool     `json:"start_clock_synced,omitempty"`
	LowBatteryReached bool      `json:"low_battery_reached"`

	// BootID is the host boot this record was written in, which the
	// *SinceBoot fields are relative to.  A different one on restore means
	// the host rebooted since.
	BootID string `json:"boot_id,omitempty"`
	// StartSinceBoot is the start as time since boot.  Within the same boot
	// it re-anchors the start on the monotonic clock, so a process-only
	// restart (a deploy) leaves the start repairable by a later clock sync.
	StartSinceBoot *time.Duration `json:"start_since_boot_ns,omitempty"`

	// Set once the outage has ended, while its summary waits to be
	// published.
	EndedAt        *time.Time `json:"ended_at,omitempty"`
	EndClockSynced *bool      `json:"end_clock_synced,omitempty"`
	// EndSinceBoot does for the end what StartSinceBoot does for the start.
	EndSinceBoot *time.Duration `json:"end_since_boot_ns,omitempty"`
	SpansRestart bool           `json:"spans_restart,omitempty"`
}

// Open describes the outage in progress, for the outage message.
type Open struct {
	StartedAt   time.Time
	ClockSynced *bool // nil where clock sync cannot be determined
}

// Summary describes an outage that has ended.
type Summary struct {
	StartedAt           time.Time
	EndedAt             time.Time
	LowBatteryReached   bool
	MinBatteryChargePct *float64 // nil when unknown, including across a restart
	ClockSynced         *bool    // nil where clock sync cannot be determined
	SpansRestart        bool

	// Final is false for an interim summary published before the clock
	// synced; a corrected one follows.
	Final bool
}

// Duration is EndedAt − StartedAt.
func (s Summary) Duration() time.Duration { return s.EndedAt.Sub(s.StartedAt) }

// episode is one outage as this process knows it.
type episode struct {
	rec Record
	// startMono anchors the start on the monotonic clock.  nil when the
	// start was restored from disk: a reboot resets the monotonic clock, so
	// a start recorded by an earlier boot cannot be re-anchored.
	startMono *time.Duration
	minCharge *float64
	// restored is true when a previous process saw part of this outage.
	restored bool
}

// ended is an outage whose summary has not yet been published finally.
type ended struct {
	ep episode
	// endMono anchors the end, when this process saw it.
	endMono *time.Duration
	// restartEnd marks an outage that ended while no process was watching.
	// After a reboot it ended at host boot; after a process-only restart, at
	// some point before this process started.
	restartEnd bool
	// rebooted says whether the host rebooted since the outage began; nil
	// when there is no boot ID to tell, and the end is chosen by comparing
	// boot time with the start instead.
	rebooted *bool
	// since is when this summary became due, on Mono, for SyncWait.
	since       time.Duration
	interimSent bool
}

// repairable reports whether the end is anchored on this boot's monotonic
// clock, so a later clock sync can correct it.
func (e *ended) repairable() bool { return e.endMono != nil || e.restartEnd }

// Tracker follows outages.  It is not safe for concurrent use; the poll loop
// owns it.
type Tracker struct {
	clock Clock
	store Store
	// SyncWait is how long a summary waits for a clock sync before an
	// interim one is published.
	SyncWait time.Duration

	restoreDone bool
	open        *episode
	pending     *ended
	// superseded counts summaries replaced before any was published, so a
	// burst of them is logged once rather than per outage.
	superseded int
}

// NewTracker returns a Tracker persisting to store.
func NewTracker(clock Clock, store Store) *Tracker {
	return &Tracker{clock: clock, store: store, SyncWait: DefaultSyncWait}
}

// Restore picks up an outage a previous process left on disk.  Call it on
// every poll, before anything else, with whether the UPS is on battery; only
// the first call does anything.
//
//   - An outage that had ended but was not yet reported is reported as
//     stored.
//   - An outage still open on disk, recorded in an earlier boot that saw low
//     battery, ended at that boot: the battery ran flat, the host died, and
//     mains returning booted it.  It is reported with spans_restart, and if
//     the UPS is on battery again now, that is a new outage.
//   - Otherwise, an outage still open on disk while the UPS is on battery is
//     resumed with its original start.  Across a reboot without low battery
//     that is a guess — the host may have rebooted on UPS power — but the
//     defensible one.
//   - An outage still open on disk while the UPS is on mains ended while no
//     process was watching.  It is reported with spans_restart, ending at
//     host boot if the host rebooted, else at this process's start.
func (t *Tracker) Restore(onBattery bool) {
	if t.restoreDone {
		return
	}
	t.restoreDone = true

	rec, err := t.store.Load()
	if errors.Is(err, ErrCorrupt) {
		log.Printf("outage state corrupt, discarding it: %v", err)
		t.remove()
		return
	}
	if err != nil {
		// Possibly transient (a failing SD card): leave the file for a
		// later process rather than destroy a real outage.
		log.Printf("outage state unreadable, ignoring it for now: %v", err)
		return
	}
	if rec == nil {
		return
	}

	ep := episode{rec: *rec, restored: true}
	rebooted := t.rebootedSince(rec)
	sameBoot := rebooted != nil && !*rebooted
	started := rec.StartedAt.Format(time.RFC3339)

	switch {
	case rec.EndedAt != nil:
		ep.restored = rec.SpansRestart
		p := &ended{ep: ep, since: t.clock.Mono()}
		// Re-anchor both ends or neither: one end on the monotonic clock and
		// the other a stored wall time would disagree after a clock step.
		if sameBoot {
			start, end := t.reanchor(rec.StartSinceBoot), t.reanchor(rec.EndSinceBoot)
			if start != nil && end != nil {
				p.ep.startMono, p.endMono = start, end
			}
		}
		t.pending = p
		log.Printf("outage started %s ended before this process started and is still unreported", started)
	case isTrue(rebooted) && rec.LowBatteryReached:
		t.pending = &ended{ep: ep, restartEnd: true, rebooted: rebooted, since: t.clock.Mono()}
		log.Printf("outage started %s ran the battery low and the host has rebooted since; it ended at boot", started)
	case onBattery:
		if sameBoot {
			ep.startMono = t.reanchor(rec.StartSinceBoot)
		} else if isTrue(rebooted) {
			// The outage continues in this boot, so the record now belongs
			// to it, with no start anchor here.  Left under the old boot ID,
			// low battery seen from now on would read, after a mere process
			// restart, as an earlier boot that ran flat — ending this outage
			// while the power is still off.
			ep.rec.BootID = t.clock.BootID()
			ep.rec.StartSinceBoot = nil
			t.save(ep.rec)
		}
		t.open = &ep
		if isTrue(rebooted) {
			log.Printf("resuming outage started %s across a reboot (low battery was never seen, so the host may have rebooted on UPS power)", started)
		} else {
			log.Printf("resuming outage started %s by a previous process", started)
		}
	default:
		if sameBoot {
			ep.startMono = t.reanchor(rec.StartSinceBoot)
		}
		t.pending = &ended{ep: ep, restartEnd: true, rebooted: rebooted, since: t.clock.Mono()}
		log.Printf("outage started %s ended while ups-mqtt was not running", started)
	}
}

// rebootedSince reports whether the host has rebooted since rec was
// written, or nil if there is no boot ID to tell by.
func (t *Tracker) rebootedSince(rec *Record) *bool {
	now := t.clock.BootID()
	if now == "" || rec.BootID == "" {
		return nil
	}
	r := now != rec.BootID
	return &r
}

// reanchor puts a time recorded earlier in this boot, as time since boot,
// back on the monotonic clock, or returns nil if it cannot.
func (t *Tracker) reanchor(sinceBoot *time.Duration) *time.Duration {
	boot, ok := t.clock.BootMono()
	if !ok || sinceBoot == nil {
		return nil
	}
	m := boot + *sinceBoot
	return &m
}

// sinceBoot is the monotonic reading m as time since boot, or nil if boot is
// unknown.
func (t *Tracker) sinceBoot(m *time.Duration) *time.Duration {
	boot, ok := t.clock.BootMono()
	if !ok || m == nil {
		return nil
	}
	d := *m - boot
	return &d
}

// OnBattery records a poll on battery.  isNew is true when this poll starts
// a new outage.
func (t *Tracker) OnBattery(lowBattery bool, charge *float64) (o Open, isNew bool) {
	if t.open == nil {
		mono := t.clock.Mono()
		t.open = &episode{
			rec: Record{
				StartedAt:         t.clock.Now(),
				StartClockSynced:  t.syncedNow(),
				LowBatteryReached: lowBattery,
				BootID:            t.clock.BootID(),
			},
			startMono: &mono,
		}
		t.open.rec.StartSinceBoot = t.sinceBoot(&mono)
		t.save(t.open.rec)
		isNew = true
	} else if lowBattery && !t.open.rec.LowBatteryReached {
		t.open.rec.LowBatteryReached = true
		t.save(t.open.rec)
	}

	// The minimum charge is tracked in memory only.  Persisting it would mean
	// a write on every change, and after a restart it is unknown anyway.
	if charge != nil && !t.open.restored && (t.open.minCharge == nil || *charge < *t.open.minCharge) {
		c := *charge
		t.open.minCharge = &c
	}

	return Open{StartedAt: t.startWall(t.open), ClockSynced: t.startSynced(t.open)}, isNew
}

// Current returns the outage in progress, if any.
func (t *Tracker) Current() (Open, bool) {
	if t.open == nil {
		return Open{}, false
	}
	return Open{StartedAt: t.startWall(t.open), ClockSynced: t.startSynced(t.open)}, true
}

// OnMains records a poll on mains.  It reports whether this poll ended an
// outage.
func (t *Tracker) OnMains() bool {
	if t.open == nil {
		return false
	}
	ep := *t.open
	t.open = nil

	mono := t.clock.Mono()
	end := t.clock.Now()
	// Persist the end, so a restart before the summary is published does not
	// mistake this for an outage still in progress.  The start is stored as
	// best known now, which a clock sync during the outage has corrected.
	ep.rec.StartedAt = t.startWall(&ep)
	ep.rec.StartClockSynced = t.startSynced(&ep)
	ep.rec.EndedAt = &end
	ep.rec.EndClockSynced = t.syncedNow()
	ep.rec.SpansRestart = ep.restored
	// Both ends relative to this boot, so a restart before the summary is
	// published can re-anchor them together.  A start carried over from an
	// earlier boot has no anchor in this one.
	ep.rec.BootID = t.clock.BootID()
	ep.rec.StartSinceBoot = t.sinceBoot(ep.startMono)
	ep.rec.EndSinceBoot = t.sinceBoot(&mono)
	t.save(ep.rec)

	// last_outage is the most recent outage.  If the previous one's summary
	// never got out — its publishes failed throughout this outage too — it
	// is superseded, not queued: say so rather than lose it silently.
	// Power flapping faster than the poll interval does the same: each
	// short outage replaces the last before a publish.
	if old := t.pending; old != nil {
		if t.superseded == 0 {
			log.Printf("dropping unpublished summary of the outage started %s; superseded by the one that just ended "+
				"(its publishes failed, or power is flapping faster than the poll interval)",
				t.startWall(&old.ep).UTC().Format(time.RFC3339))
		}
		t.superseded++
	}
	t.pending = &ended{ep: ep, endMono: &mono, since: mono}
	return true
}

// Due returns the summary to publish now, if there is one.  After a
// successful publish, call Published with it; until then, Due keeps
// returning it.
//
// A summary is final once the wall clock is synced (or sync cannot be
// determined, or the end cannot be repaired anyway).  Until then it waits up
// to SyncWait, after which one interim summary goes out marked
// clock_synced: false.  A new outage starting forces the summary out, final:
// the state file is needed for the new outage.
func (t *Tracker) Due() (Summary, bool) {
	p := t.pending
	if p == nil {
		return Summary{}, false
	}
	s := t.summarise(p)
	synced, known := t.clock.Synced()
	switch {
	case !known || synced || !p.repairable() || t.open != nil:
		s.Final = true
	case !p.interimSent && t.clock.Mono()-p.since >= t.SyncWait:
		s.Final = false
	default:
		return Summary{}, false
	}
	return s, true
}

// Published records that s was published successfully.  A final summary is
// done with, and the state file is removed (unless a new outage now owns
// it).  After an interim one, the state is kept so the corrected summary can
// follow — even across another restart.
func (t *Tracker) Published(s Summary) {
	if t.pending == nil {
		return
	}
	if !s.Final {
		t.pending.interimSent = true
		return
	}
	t.pending = nil
	if t.superseded > 1 {
		log.Printf("%d outage summaries were superseded before this one was published; last_outage reports the latest", t.superseded)
	}
	t.superseded = 0
	if t.open == nil {
		t.remove()
	}
}

// summarise works out a summary from what is known now.
func (t *Tracker) summarise(p *ended) Summary {
	start := t.startWall(&p.ep)

	var end time.Time
	switch {
	case p.endMono != nil:
		end = t.wallAt(*p.endMono)
	case p.restartEnd:
		// Mains returning is what brings a host back after its UPS ran flat,
		// so boot time is the best estimate of the end.  If only the process
		// restarted, the outage ended at some point before this process
		// started.  The boot ID says which; without one, a boot after the
		// start means a reboot.
		end = t.wallAt(0)
		if boot, ok := t.clock.BootMono(); ok {
			bootWall := t.wallAt(boot)
			if (p.rebooted != nil && *p.rebooted) || (p.rebooted == nil && bootWall.After(start)) {
				end = bootWall
			}
		}
	default:
		end = *p.ep.rec.EndedAt
	}
	if end.Before(start) {
		end = start // a clock that was badly wrong at the start
	}

	var clockSynced *bool
	if synced, known := t.clock.Synced(); known {
		ok := synced && isTrue(t.startSynced(&p.ep))
		if !p.repairable() {
			ok = ok && isTrue(p.ep.rec.EndClockSynced)
		}
		clockSynced = &ok
	}

	var minCharge *float64
	if !p.ep.restored {
		minCharge = p.ep.minCharge
	}

	return Summary{
		StartedAt:           start.UTC(),
		EndedAt:             end.UTC(),
		LowBatteryReached:   p.ep.rec.LowBatteryReached,
		MinBatteryChargePct: minCharge,
		ClockSynced:         clockSynced,
		SpansRestart:        p.ep.restored || p.restartEnd,
	}
}

// wallAt is the wall time of the monotonic reading m, as best known now.
func (t *Tracker) wallAt(m time.Duration) time.Time {
	return t.clock.Now().Add(m - t.clock.Mono())
}

// startWall is the outage's start on the wall clock, as best known now.
func (t *Tracker) startWall(ep *episode) time.Time {
	if ep.startMono != nil {
		return t.wallAt(*ep.startMono)
	}
	return ep.rec.StartedAt
}

// startSynced reports whether the start time is right: always, once the
// clock is synced, for a start anchored in this process; for one restored
// from disk, only if the clock was synced when it was recorded.
func (t *Tracker) startSynced(ep *episode) *bool {
	synced, known := t.clock.Synced()
	if !known {
		return nil
	}
	ok := synced
	if ep.startMono == nil {
		ok = ok && isTrue(ep.rec.StartClockSynced)
	}
	return &ok
}

func (t *Tracker) syncedNow() *bool {
	synced, known := t.clock.Synced()
	if !known {
		return nil
	}
	return &synced
}

func (t *Tracker) save(rec Record) {
	if err := t.store.Save(rec); err != nil {
		log.Printf("saving outage state: %v", err)
	}
}

func (t *Tracker) remove() {
	if err := t.store.Remove(); err != nil {
		log.Printf("removing outage state: %v", err)
	}
}

func isTrue(b *bool) bool { return b != nil && *b }
