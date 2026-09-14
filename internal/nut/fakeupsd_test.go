package nut

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeUPSD is an in-process upsd speaking the real NUT protocol, so tests
// exercise the actual wire format rather than a mock of it.
//
// Behaviour is configured by the exported fields before Start; the zero value
// serves an empty variable list with no authentication.
type fakeUPSD struct {
	// UPSName is the device the server answers for. LIST VAR for any other
	// name is rejected with ERR UNKNOWN-UPS, exactly as upsd does.
	UPSName string
	// Lines are the raw VAR lines returned by LIST VAR, verbatim. Using raw
	// lines rather than a map lets tests feed real captured fixtures, and
	// malformed input the parser must reject.
	Lines []string

	// Username and Password, when set, are required: commands before a
	// successful login are refused with ERR ACCESS-DENIED.
	Username string
	Password string

	// ErrorFor maps a command to an error code returned instead of a normal
	// reply, e.g. "LIST VAR ups" -> "DATA-STALE".
	ErrorFor map[string]string

	// DropAfter closes the connection after this many commands, simulating
	// upsd restarting or the connection being reset. Zero means never.
	DropAfter int
	// DelayBefore pauses before replying to the nth command (1-based), for
	// exercising timeouts. Zero means never.
	DelayBefore int
	Delay       time.Duration
	// Silent makes the server accept connections but never reply, so the
	// client must rely on its own deadline.
	Silent bool

	ln net.Listener

	mu       sync.Mutex
	accepted int
	openNow  int
	commands []string
}

func newFakeUPSD(t *testing.T) *fakeUPSD {
	t.Helper()
	return &fakeUPSD{UPSName: "testups"}
}

func (s *fakeUPSD) start(t *testing.T) *fakeUPSD {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s.ln = ln
	go s.acceptLoop()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *fakeUPSD) port() int { return s.ln.Addr().(*net.TCPAddr).Port }

func (s *fakeUPSD) acceptLoop() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.accepted++
		s.openNow++
		s.mu.Unlock()
		go s.handle(conn)
	}
}

func (s *fakeUPSD) handle(conn net.Conn) {
	defer func() {
		_ = conn.Close()
		s.mu.Lock()
		s.openNow--
		s.mu.Unlock()
	}()

	r := bufio.NewReader(conn)
	authed := s.Username == ""
	n := 0

	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return // client closed, which is what we want to observe
		}
		cmd := strings.TrimRight(line, "\r\n")
		n++

		s.mu.Lock()
		s.commands = append(s.commands, cmd)
		s.mu.Unlock()

		if s.Silent {
			continue
		}
		if s.DelayBefore == n && s.Delay > 0 {
			time.Sleep(s.Delay)
		}
		if s.DropAfter > 0 && n > s.DropAfter {
			return
		}

		reply, done := s.replyTo(cmd, &authed)
		if reply != "" {
			_, _ = fmt.Fprint(conn, reply)
		}
		if done {
			return
		}
	}
}

// replyTo renders the response for one command. The bool reports whether the
// connection should then close (LOGOUT).
func (s *fakeUPSD) replyTo(cmd string, authed *bool) (string, bool) {
	if code, ok := s.ErrorFor[cmd]; ok {
		return "ERR " + code + "\n", false
	}

	switch {
	case cmd == "LOGOUT":
		return "OK Goodbye\n", true

	case strings.HasPrefix(cmd, "USERNAME "):
		if strings.TrimPrefix(cmd, "USERNAME ") != s.Username {
			return "ERR ACCESS-DENIED\n", false
		}
		return "OK\n", false

	case strings.HasPrefix(cmd, "PASSWORD "):
		if strings.TrimPrefix(cmd, "PASSWORD ") != s.Password {
			return "ERR ACCESS-DENIED\n", false
		}
		*authed = true
		return "OK\n", false

	case strings.HasPrefix(cmd, "LIST VAR "):
		if !*authed {
			return "ERR ACCESS-DENIED\n", false
		}
		name := strings.TrimPrefix(cmd, "LIST VAR ")
		if name != s.UPSName {
			return "ERR UNKNOWN-UPS\n", false
		}
		var b strings.Builder
		fmt.Fprintf(&b, "BEGIN %s\n", cmd)
		for _, l := range s.Lines {
			fmt.Fprintf(&b, "%s\n", l)
		}
		fmt.Fprintf(&b, "END %s\n", cmd)
		return b.String(), false
	}
	return "ERR UNKNOWN-COMMAND\n", false
}

func (s *fakeUPSD) stats() (accepted, open int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.accepted, s.openNow
}

func (s *fakeUPSD) sent() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.commands...)
}

// client returns a Client pointed at this server, with a short timeout so
// failure cases do not stall the suite.
func (s *fakeUPSD) client(t *testing.T, user, pass string) *Client {
	t.Helper()
	c, err := NewClient("127.0.0.1", s.port(), user, pass, s.UPSName)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	c.Timeout = 2 * time.Second
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// varLinesFromFixture loads captured upsd output and returns just its VAR
// lines, so tests run against bytes a real device actually produced.
func varLinesFromFixture(t *testing.T, name string) []string {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	var out []string
	for _, l := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(l, "VAR ") {
			out = append(out, l)
		}
	}
	if len(out) == 0 {
		t.Fatalf("fixture %s contained no VAR lines", name)
	}
	return out
}

// upsNameFromFixture reads the device name out of a fixture's BEGIN line.
func upsNameFromFixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	for _, l := range strings.Split(string(raw), "\n") {
		if rest, ok := strings.CutPrefix(l, "BEGIN LIST VAR "); ok {
			return strings.TrimSpace(rest)
		}
	}
	t.Fatalf("fixture %s has no BEGIN LIST VAR line", name)
	return ""
}
