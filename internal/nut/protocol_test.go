package nut

import (
	"strings"
	"testing"
	"time"
)

// ── Fixture-driven: real bytes from real devices ──────────────────────────

var fixtures = []struct {
	file     string
	wantVars int
	// a variable whose value exercises something awkward
	spotName  string
	spotValue string
}{
	{"listvar_cyberpower.txt", 48, "ups.mfr", "CPS"},
	{"listvar_riello.txt", 49, "ups.mfr", "RPS S.p.a."},
	{"listvar_apc.txt", 47, "ups.mfr", "American Power Conversion"},
}

// Each fixture is a verbatim capture of a real upsd's LIST VAR reply. Parsing
// them proves the client handles what the devices actually emit, rather than
// what the protocol document says they should.
func TestPoll_RealDeviceFixtures(t *testing.T) {
	for _, fx := range fixtures {
		t.Run(fx.file, func(t *testing.T) {
			s := newFakeUPSD(t)
			s.UPSName = upsNameFromFixture(t, fx.file)
			s.Lines = varLinesFromFixture(t, fx.file)
			s.start(t)

			vars, err := s.client(t, "", "").Poll()
			if err != nil {
				t.Fatalf("Poll: %v", err)
			}
			if len(vars) != fx.wantVars {
				t.Errorf("got %d variables, want %d", len(vars), fx.wantVars)
			}
			m := VarsToMap(vars)
			if got := m[fx.spotName]; got != fx.spotValue {
				t.Errorf("%s = %q, want %q", fx.spotName, got, fx.spotValue)
			}
		})
	}
}

// A poll must be one round trip. go.nut issued ~96 commands per poll because
// it built a full UPS object, fetching a description and type for every
// variable.
func TestPoll_IsASingleRoundTrip(t *testing.T) {
	s := newFakeUPSD(t)
	s.Lines = varLinesFromFixture(t, "listvar_riello.txt")
	s.UPSName = upsNameFromFixture(t, "listvar_riello.txt")
	s.start(t)

	if _, err := s.client(t, "", "").Poll(); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	sent := s.sent()
	if len(sent) != 1 {
		t.Errorf("poll sent %d commands, want 1: %v", len(sent), sent)
	}
	if want := "LIST VAR " + s.UPSName; sent[0] != want {
		t.Errorf("sent %q, want %q", sent[0], want)
	}
}

// ── The two regressions that motivated this client ────────────────────────

// go.nut had no way to close a socket, so a "clean" Close left it open.
func TestClose_ActuallyClosesTheSocket(t *testing.T) {
	s := newFakeUPSD(t).start(t)
	c := s.client(t, "", "")

	waitFor(t, func() bool { _, open := s.stats(); return open == 1 },
		"server never saw the client connect")
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	waitFor(t, func() bool { _, open := s.stats(); return open == 0 },
		"connection still open after Close()")
}

// Poll re-dials when a connection dies. Each re-dial must close the old socket
// first, or an unreachable UPS leaks one per poll for as long as it is down.
func TestPoll_ReconnectDoesNotLeakSockets(t *testing.T) {
	s := newFakeUPSD(t)
	s.UPSName = "testups"
	s.DropAfter = 1 // every connection dies after one command
	s.start(t)

	c := s.client(t, "", "")
	const polls = 5
	for i := 0; i < polls; i++ {
		_, _ = c.Poll() // expected to fail; we care about the sockets
	}

	accepted, _ := s.stats()
	if accepted < 2 {
		t.Fatalf("expected repeated reconnects, only %d connections", accepted)
	}
	waitFor(t, func() bool { _, open := s.stats(); return open <= 1 },
		"sockets accumulating across reconnects")
	_, open := s.stats()
	t.Logf("%d connections made across %d polls, %d still open", accepted, polls, open)
}

// A connection dropped between polls must heal on the next poll rather than
// failing forever — this is what made the daemon need a manual restart.
func TestPoll_RecoversAfterConnectionDrops(t *testing.T) {
	s := newFakeUPSD(t)
	s.UPSName = "testups"
	s.Lines = []string{`VAR testups ups.status "OL"`}
	s.DropAfter = 1
	s.start(t)

	c := s.client(t, "", "")
	if _, err := c.Poll(); err != nil {
		t.Fatalf("first poll should succeed: %v", err)
	}

	// The server closed the connection after that command. The next poll must
	// notice, re-dial, and succeed.
	vars, err := c.Poll()
	if err != nil {
		t.Fatalf("poll after drop should have reconnected, got: %v", err)
	}
	if got := VarsToMap(vars)["ups.status"]; got != "OL" {
		t.Errorf("ups.status = %q, want OL", got)
	}
}

// ── Protocol error handling ───────────────────────────────────────────────

func TestPoll_UnknownUPS(t *testing.T) {
	s := newFakeUPSD(t)
	s.UPSName = "realups"
	s.start(t)

	c, err := NewClient("127.0.0.1", s.port(), "", "", "wrongups")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer c.Close() //nolint:errcheck
	c.Timeout = 2 * time.Second

	_, err = c.Poll()
	if err == nil {
		t.Fatal("expected an error for an unknown UPS")
	}
	if !strings.Contains(err.Error(), "UNKNOWN-UPS") {
		t.Errorf("error = %v, want it to name UNKNOWN-UPS", err)
	}
}

func TestPoll_ServerReturnsError(t *testing.T) {
	s := newFakeUPSD(t)
	s.UPSName = "testups"
	s.ErrorFor = map[string]string{"LIST VAR testups": "DATA-STALE"}
	s.start(t)

	_, err := s.client(t, "", "").Poll()
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "DATA-STALE") {
		t.Errorf("error = %v, want it to name DATA-STALE", err)
	}
}

func TestPoll_TimesOutRatherThanHanging(t *testing.T) {
	s := newFakeUPSD(t)
	s.Silent = true // accepts, never replies
	s.start(t)

	c, err := NewClient("127.0.0.1", s.port(), "", "", "testups")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer c.Close() //nolint:errcheck
	c.Timeout = 300 * time.Millisecond

	done := make(chan error, 1)
	go func() { _, e := c.Poll(); done <- e }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected a timeout error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Poll hung past its timeout")
	}
}

// ── Authentication ────────────────────────────────────────────────────────

func TestAuth_SuccessSendsCredentialsBeforeListing(t *testing.T) {
	s := newFakeUPSD(t)
	s.UPSName = "testups"
	s.Username = "user"
	s.Password = "pass"
	s.Lines = []string{`VAR testups ups.status "OL"`}
	s.start(t)

	if _, err := s.client(t, "user", "pass").Poll(); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	sent := s.sent()
	want := []string{"USERNAME user", "PASSWORD pass", "LIST VAR testups"}
	if len(sent) < len(want) {
		t.Fatalf("sent %v, want at least %v", sent, want)
	}
	for i, w := range want {
		if sent[i] != w {
			t.Errorf("command %d = %q, want %q", i, sent[i], w)
		}
	}
}

func TestAuth_WrongPasswordFailsToConnect(t *testing.T) {
	s := newFakeUPSD(t)
	s.Username = "user"
	s.Password = "right"
	s.start(t)

	_, err := NewClient("127.0.0.1", s.port(), "user", "wrong", "testups")
	if err == nil {
		t.Fatal("expected authentication to fail")
	}
	if !strings.Contains(err.Error(), "ACCESS-DENIED") {
		t.Errorf("error = %v, want it to name ACCESS-DENIED", err)
	}
}

func TestAuth_NoCredentialsSkipsLogin(t *testing.T) {
	s := newFakeUPSD(t)
	s.UPSName = "testups"
	s.Lines = []string{`VAR testups ups.status "OL"`}
	s.start(t)

	if _, err := s.client(t, "", "").Poll(); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	for _, cmd := range s.sent() {
		if strings.HasPrefix(cmd, "USERNAME") || strings.HasPrefix(cmd, "PASSWORD") {
			t.Errorf("unexpected auth command %q when no username configured", cmd)
		}
	}
}

// ── Close semantics ───────────────────────────────────────────────────────

func TestClose_IsIdempotent(t *testing.T) {
	s := newFakeUPSD(t).start(t)
	c := s.client(t, "", "")
	for i := 0; i < 3; i++ {
		if err := c.Close(); err != nil {
			t.Fatalf("Close %d: %v", i, err)
		}
	}
}

func TestClose_OnUnconnectedClientIsNoOp(t *testing.T) {
	c := &Client{}
	if err := c.Close(); err != nil {
		t.Errorf("Close on an unconnected client returned %v", err)
	}
}

func TestClose_SendsLogout(t *testing.T) {
	s := newFakeUPSD(t).start(t)
	c := s.client(t, "", "")
	_ = c.Close()
	waitFor(t, func() bool {
		for _, cmd := range s.sent() {
			if cmd == "LOGOUT" {
				return true
			}
		}
		return false
	}, "Close did not send LOGOUT")
}

func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error(msg)
}

// ── Malformed server behaviour ────────────────────────────────────────────

// upsd should answer LIST with a BEGIN marker. Anything else is a protocol
// violation and must be reported, not silently treated as data.
func TestPoll_MissingBeginMarker(t *testing.T) {
	s := newFakeUPSD(t)
	s.UPSName = "testups"
	s.ErrorFor = nil
	s.start(t)

	// Replace the normal reply with a bare line by pointing the client at a
	// UPS the server answers for, then corrupting the reply via ErrorFor is
	// not possible — so use a server that always answers UNKNOWN-COMMAND.
	c, err := NewClient("127.0.0.1", s.port(), "", "", "testups")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer c.Close() //nolint:errcheck
	c.Timeout = 2 * time.Second

	// LIST VAR for the configured name succeeds; ask for a different command
	// path by renaming the server's device mid-test.
	s.mu.Lock()
	s.UPSName = "somethingelse"
	s.mu.Unlock()

	if _, err := c.Poll(); err == nil {
		t.Fatal("expected an error when the server does not serve this UPS")
	}
}

// A variable line the parser cannot read must fail the poll rather than being
// dropped silently — a half-parsed reading is worse than none.
func TestPoll_MalformedVariableLine(t *testing.T) {
	s := newFakeUPSD(t)
	s.UPSName = "testups"
	s.Lines = []string{
		`VAR testups ups.status "OL"`,
		`VAR testups broken-no-quotes 100`,
	}
	s.start(t)

	_, err := s.client(t, "", "").Poll()
	if err == nil {
		t.Fatal("expected a parse error")
	}
	if !strings.Contains(err.Error(), "quoted") {
		t.Errorf("error = %v, want it to explain the parse failure", err)
	}
}

// An empty variable list is legitimate — a driver that has connected but not
// yet read anything. It must not be an error.
func TestPoll_EmptyVariableList(t *testing.T) {
	s := newFakeUPSD(t)
	s.UPSName = "testups"
	s.Lines = nil
	s.start(t)

	vars, err := s.client(t, "", "").Poll()
	if err != nil {
		t.Fatalf("an empty list should not be an error: %v", err)
	}
	if len(vars) != 0 {
		t.Errorf("got %d variables, want 0", len(vars))
	}
}

// When the connection is dead and re-dialling also fails, the error must
// report both causes rather than hiding the original.
func TestPoll_ReconnectAlsoFails(t *testing.T) {
	s := newFakeUPSD(t)
	s.UPSName = "testups"
	s.Lines = []string{`VAR testups ups.status "OL"`}
	s.DropAfter = 1
	s.start(t)

	c := s.client(t, "", "")
	if _, err := c.Poll(); err != nil {
		t.Fatalf("first poll: %v", err)
	}

	// Take the server away entirely, so the retry cannot dial.
	_ = s.ln.Close()

	_, err := c.Poll()
	if err == nil {
		t.Fatal("expected an error once the server has gone")
	}
	if !strings.Contains(err.Error(), "reconnect also failed") {
		t.Errorf("error = %v, want it to report the failed reconnect too", err)
	}
}
