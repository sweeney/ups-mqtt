package outages

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileStore_RoundTrip(t *testing.T) {
	s := &FileStore{Path: filepath.Join(t.TempDir(), StateFileName)}
	if rec, err := s.Load(); rec != nil || err != nil {
		t.Fatalf("Load() on no file = %v, %v; want nil, nil", rec, err)
	}

	synced := true
	end := t0.Add(time.Hour)
	want := Record{StartedAt: t0, StartClockSynced: &synced, LowBatteryReached: true, EndedAt: &end, SpansRestart: true}
	if err := s.Save(want); err != nil {
		t.Fatalf("Save() = %v", err)
	}
	if _, err := os.Stat(s.Path + ".tmp"); !os.IsNotExist(err) {
		t.Error("temp file left behind")
	}
	got, err := s.Load()
	if err != nil || got == nil {
		t.Fatalf("Load() = %v, %v", got, err)
	}
	if !got.StartedAt.Equal(want.StartedAt) || !got.EndedAt.Equal(end) || !isTrue(got.StartClockSynced) ||
		!got.LowBatteryReached || !got.SpansRestart || got.EndClockSynced != nil {
		t.Errorf("Load() = %+v, want %+v", got, want)
	}

	if err := s.Remove(); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	if err := s.Remove(); err != nil {
		t.Errorf("Remove() of a missing file = %v, want nil", err)
	}
	if rec, _ := s.Load(); rec != nil {
		t.Error("record still there after Remove")
	}
}

func TestFileStore_CorruptFile(t *testing.T) {
	for _, body := range []string{`{"started_at":`, `{}`} {
		s := &FileStore{Path: filepath.Join(t.TempDir(), StateFileName)}
		if err := os.WriteFile(s.Path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Load(); err == nil || !strings.Contains(err.Error(), s.Path) {
			t.Errorf("Load(%q) = %v, want an error naming the file", body, err)
		}
	}
}

func TestFileStore_MissingDirectory(t *testing.T) {
	s := &FileStore{Path: filepath.Join(t.TempDir(), "gone", StateFileName)}
	if err := s.Save(Record{StartedAt: t0}); err == nil {
		t.Error("Save() into a missing directory succeeded")
	}
}

func TestNopStore(t *testing.T) {
	var s NopStore
	if err := s.Save(Record{StartedAt: t0}); err != nil {
		t.Error(err)
	}
	if rec, err := s.Load(); rec != nil || err != nil {
		t.Errorf("Load() = %v, %v", rec, err)
	}
	if err := s.Remove(); err != nil {
		t.Error(err)
	}
}

func TestParseUptime(t *testing.T) {
	d, err := parseUptime("350735.47 234388.90\n")
	if err != nil || d != 350735470*time.Millisecond {
		t.Errorf("parseUptime = %s, %v", d, err)
	}
	for _, bad := range []string{"", "abc 1"} {
		if _, err := parseUptime(bad); err == nil {
			t.Errorf("parseUptime(%q) succeeded", bad)
		}
	}
}

// The real clock: Mono only moves forward, and BootMono, when known, is in
// the past.
func TestSystemClock(t *testing.T) {
	c := NewSystemClock()
	a := c.Mono()
	time.Sleep(time.Millisecond)
	if b := c.Mono(); b <= a {
		t.Errorf("Mono went %s → %s", a, b)
	}
	if boot, ok := c.BootMono(); ok && boot >= 0 {
		t.Errorf("BootMono = %s, want negative", boot)
	}
	if d := time.Since(c.Now()); d < 0 || d > time.Second {
		t.Errorf("Now is %s off", d)
	}
	c.Synced() // must not panic on any OS
}
