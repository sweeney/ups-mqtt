package nut

import (
	"errors"
	"fmt"
	"net"
	"testing"
	"time"
)

// Cause reduces a failed poll to a stable, coarse code for publishing: the
// full error names upsd's address, and after a failed re-dial a new local
// port every time, so it would change every poll and could carry a LAN
// address to an off-site broker.  upsd's own code where it sent one, else
// what the network did.

func TestCause_UpsdCodeThroughPoll(t *testing.T) {
	for _, code := range []string{"DRIVER-NOT-CONNECTED", "DATA-STALE"} {
		t.Run(code, func(t *testing.T) {
			s := newFakeUPSD(t)
			s.ErrorFor = map[string]string{"LIST VAR testups": code}
			s.start(t)
			_, err := s.client(t, "", "").Poll()
			if got := Cause(err); got != code {
				t.Errorf("Cause(%v) = %q, want %q", err, got, code)
			}
			var pe *ProtocolError
			if !errors.As(err, &pe) || pe.Code != code {
				t.Errorf("errors.As(%v, *ProtocolError) = %v, %+v", err, errors.As(err, &pe), pe)
			}
		})
	}
}

// The error's text is unchanged, so the log reads as it did.
func TestProtocolError_Text(t *testing.T) {
	if got := protocolError("ERR DATA-STALE").Error(); got != "upsd error: DATA-STALE" {
		t.Errorf("Error() = %q", got)
	}
}

func TestCause_Refused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	_, err = NewClient("127.0.0.1", port, "", "", "testups")
	if got := Cause(err); got != "refused" {
		t.Errorf("Cause(%v) = %q, want refused", err, got)
	}
}

func TestCause_Timeout(t *testing.T) {
	s := newFakeUPSD(t)
	s.Silent = true
	s.start(t)
	c, err := NewClient("127.0.0.1", s.port(), "", "", "testups")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Timeout = 100 * time.Millisecond
	_, err = c.Poll()
	if got := Cause(err); got != "timeout" {
		t.Errorf("Cause(%v) = %q, want timeout", err, got)
	}
}

// The first failure decides: Poll wraps it, and only describes the
// re-dial's.  A protocol error stays its code even after a failed re-dial.
func TestCause_FirstFailureWins(t *testing.T) {
	err := fmt.Errorf("listing variables for %q: %w (reconnect also failed: %v)",
		"x", &ProtocolError{Code: "DATA-STALE"}, errors.New("dial tcp 127.0.0.1:3493: connect: connection refused"))
	if got := Cause(err); got != "DATA-STALE" {
		t.Errorf("Cause = %q, want DATA-STALE", got)
	}
}

func TestCause_Other(t *testing.T) {
	for _, err := range []error{errors.New("expected BEGIN, got junk"), ErrNotConnected} {
		if got := Cause(err); got != "unreachable" {
			t.Errorf("Cause(%v) = %q, want unreachable", err, got)
		}
	}
	if got := Cause(nil); got != "" {
		t.Errorf("Cause(nil) = %q, want empty", got)
	}
}
