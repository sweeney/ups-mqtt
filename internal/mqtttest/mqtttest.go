// Package mqtttest runs real brokers for tests: a Mosquitto process, a TCP
// proxy in front of it that can fail the way real links do, and a recorder
// that subscribes and keeps everything it receives.
//
// Faults are injected at the TCP level against a real broker because the
// failures seen live (docs/live-tests.md) live in the interplay of paho, the
// broker and the socket: a connection that still looks open while nothing
// gets through, a reconnect that cannot resolve the broker.  No fake models
// those faithfully.
//
// Tests that need Mosquitto skip when it is not installed, except under CI
// (the CI environment variable set), where they fail: CI installs it, and a
// silently skipped fault test is no test.
package mqtttest

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// MosquittoPath finds the mosquitto binary, or skips (fails under CI).
func MosquittoPath(t testing.TB) string {
	t.Helper()
	if p, err := exec.LookPath("mosquitto"); err == nil {
		return p
	}
	for _, p := range []string{"/usr/sbin/mosquitto", "/opt/homebrew/sbin/mosquitto", "/usr/local/sbin/mosquitto"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if os.Getenv("CI") != "" {
		t.Fatal("mosquitto not found; CI must install it")
	}
	t.Skip("mosquitto not installed")
	return ""
}

// Broker is a Mosquitto process listening on 127.0.0.1.
type Broker struct {
	Addr string // host:port
	Conf string // path of the config file it runs with

	t    testing.TB
	bin  string
	cmd  *exec.Cmd
	logs *lockedBuffer
}

// StartBroker runs Mosquitto on a free local port with anonymous access and
// no persistence, plus any extra config lines.  It is stopped when the test
// ends.
func StartBroker(t testing.TB, extra string) *Broker {
	t.Helper()
	port := FreePort(t)
	conf := fmt.Sprintf("listener %d 127.0.0.1\nallow_anonymous true\npersistence false\n%s\n", port, extra)
	return StartBrokerConf(t, conf, fmt.Sprintf("127.0.0.1:%d", port))
}

// StartBrokerConf runs Mosquitto with the given config, which must listen on
// addr.  Use it to run a production config verbatim.
func StartBrokerConf(t testing.TB, conf, addr string) *Broker {
	t.Helper()
	b := &Broker{Addr: addr, t: t, bin: MosquittoPath(t), logs: &lockedBuffer{}}
	b.Conf = filepath.Join(t.TempDir(), "mosquitto.conf")
	if err := os.WriteFile(b.Conf, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	b.start()
	t.Cleanup(b.Stop)
	return b
}

func (b *Broker) start() {
	b.t.Helper()
	b.cmd = exec.Command(b.bin, "-c", b.Conf)
	b.cmd.Stdout = b.logs
	b.cmd.Stderr = b.logs
	if err := b.cmd.Start(); err != nil {
		b.t.Fatalf("starting mosquitto: %v", err)
	}
	if !waitListening(b.Addr, 5*time.Second) {
		b.Stop()
		b.t.Fatalf("mosquitto did not listen on %s:\n%s", b.Addr, b.logs.String())
	}
}

// Stop kills the broker.  It is safe to call more than once.
func (b *Broker) Stop() {
	if b.cmd == nil || b.cmd.Process == nil {
		return
	}
	_ = b.cmd.Process.Kill()
	_ = b.cmd.Wait()
	b.cmd = nil
}

// Restart stops the broker and starts it again with the same config.
func (b *Broker) Restart() {
	b.t.Helper()
	b.Stop()
	b.start()
}

// Logs returns what the broker has written so far.
func (b *Broker) Logs() string { return b.logs.String() }

// URL is the broker's address as paho takes it.
func (b *Broker) URL() string { return "tcp://" + b.Addr }

// FreePort returns a TCP port that was free a moment ago.
func FreePort(t testing.TB) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close() //nolint:errcheck
	return l.Addr().(*net.TCPAddr).Port
}

func waitListening(addr string, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond); err == nil {
			_ = c.Close()
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// lockedBuffer is an io.Writer safe for a child process and a reader.
type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// Mode is how a Proxy treats traffic.
type Mode int

const (
	// Up forwards everything.
	Up Mode = iota
	// Blackhole silently drops all bytes on open connections, which stay
	// open — a half-open link, as when a router loses power — and refuses
	// new ones, as when the broker's name no longer resolves.  It is what
	// live test 2 hit.
	Blackhole
	// Down closes every connection and refuses new ones: a broker restart
	// or a link that resets cleanly.
	Down
)

func (m Mode) String() string {
	return [...]string{"up", "blackhole", "down"}[m]
}

// Proxy is a TCP proxy in front of a broker whose link can be failed.
type Proxy struct {
	Addr string

	target string
	ln     net.Listener

	mu    sync.Mutex
	mode  Mode
	conns map[*proxyConn]struct{}
}

type proxyConn struct {
	client, server net.Conn
}

// StartProxy listens on a free local port and forwards to target.  It is
// closed when the test ends.
func StartProxy(t testing.TB, target string) *Proxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &Proxy{Addr: ln.Addr().String(), target: target, ln: ln, conns: map[*proxyConn]struct{}{}}
	go p.accept()
	t.Cleanup(p.Close)
	return p
}

// URL is the proxy's address as paho takes it.
func (p *Proxy) URL() string { return "tcp://" + p.Addr }

// SetMode changes how traffic is treated from now on.  Switching to Down
// closes every open connection.
func (p *Proxy) SetMode(m Mode) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.mode = m
	if m == Down {
		for c := range p.conns {
			c.close()
		}
	}
}

// Close stops listening and closes every connection.
func (p *Proxy) Close() {
	_ = p.ln.Close()
	p.SetMode(Down)
}

func (p *Proxy) currentMode() Mode {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.mode
}

func (p *Proxy) accept() {
	for {
		client, err := p.ln.Accept()
		if err != nil {
			return
		}
		if p.currentMode() != Up {
			_ = client.Close()
			continue
		}
		server, err := net.Dial("tcp", p.target)
		if err != nil {
			_ = client.Close()
			continue
		}
		c := &proxyConn{client: client, server: server}
		p.mu.Lock()
		p.conns[c] = struct{}{}
		p.mu.Unlock()
		go p.pipe(c, client, server)
		go p.pipe(c, server, client)
	}
}

// pipe copies src to dst, dropping bytes while blackholed.
func (p *Proxy) pipe(c *proxyConn, dst, src net.Conn) {
	defer func() {
		c.close()
		p.mu.Lock()
		delete(p.conns, c)
		p.mu.Unlock()
	}()
	buf := make([]byte, 32*1024)
	for {
		n, err := src.Read(buf)
		if n > 0 && p.currentMode() == Up {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (c *proxyConn) close() {
	_ = c.client.Close()
	_ = c.server.Close()
}

// Received is one message a Recorder saw.
type Received struct {
	At       time.Time
	Topic    string
	Payload  string
	Retained bool
}

// ReadLines reads a file of text lines, skipping blank lines and # comments.
func ReadLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		out = append(out, l)
	}
	return out, sc.Err()
}

var _ io.Writer = (*lockedBuffer)(nil)
