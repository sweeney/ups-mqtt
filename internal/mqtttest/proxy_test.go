package mqtttest

import (
	"io"
	"net"
	"testing"
	"time"
)

// echoServer accepts connections and echoes what it reads.
func echoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() }) //nolint:errcheck
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go io.Copy(c, c) //nolint:errcheck
		}
	}()
	return ln.Addr().String()
}

// roundTrip writes a byte and reports whether it came back in time.
func roundTrip(t *testing.T, c net.Conn) bool {
	t.Helper()
	if _, err := c.Write([]byte{'x'}); err != nil {
		return false
	}
	c.SetReadDeadline(time.Now().Add(200 * time.Millisecond)) //nolint:errcheck
	b := make([]byte, 1)
	_, err := c.Read(b)
	return err == nil
}

func TestProxy_Modes(t *testing.T) {
	p := StartProxy(t, echoServer(t))
	c, err := net.Dial("tcp", p.Addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close() //nolint:errcheck

	if !roundTrip(t, c) {
		t.Fatal("no echo through an Up proxy")
	}

	// Blackhole: the open connection stays open but nothing gets through,
	// and new connections are refused.
	p.SetMode(Blackhole)
	if roundTrip(t, c) {
		t.Error("data got through a blackholed link")
	}
	if c2, err := net.Dial("tcp", p.Addr); err == nil {
		c2.SetReadDeadline(time.Now().Add(time.Second)) //nolint:errcheck
		if _, err := c2.Read(make([]byte, 1)); err == nil {
			t.Error("new connection accepted while blackholed")
		}
		c2.Close() //nolint:errcheck
	}

	// Up again: a new connection works.
	p.SetMode(Up)
	c3, err := net.Dial("tcp", p.Addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c3.Close() //nolint:errcheck
	if !roundTrip(t, c3) {
		t.Error("no echo after the link came back")
	}

	// Down closes what is open.
	p.SetMode(Down)
	c3.SetReadDeadline(time.Now().Add(time.Second)) //nolint:errcheck
	if _, err := c3.Read(make([]byte, 1)); err == nil {
		t.Error("connection still open after Down")
	}
}
