package nut

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// DefaultTimeout bounds every network operation: the initial dial, and each
// command's write and read. A poll that cannot finish before the next one
// begins is of no use, and upsd is invariably on loopback.
const DefaultTimeout = 5 * time.Second

// ErrNotConnected is returned when an operation is attempted without a live
// connection and re-dialling also failed.
var ErrNotConnected = errors.New("not connected to upsd")

// Client speaks the NUT (Network UPS Tools) upsd protocol directly.
//
// It replaces github.com/robbiet480/go.nut, which cannot close a socket: that
// library exposes no Close method, keeps its connection in an unexported
// field, and its Disconnect only sends LOGOUT without closing anything. Since
// Poll re-dials whenever a connection goes stale, every failed poll leaked a
// socket — one per poll for as long as the UPS was unreachable.
//
// Speaking the protocol directly also collapses a poll to a single round trip.
// go.nut's GetUPSList built a full UPS object per device, issuing GET DESC and
// GET TYPE for every variable — around 96 commands per poll where one suffices.
//
// A Client is not safe for concurrent use.
type Client struct {
	host     string
	port     int
	username string
	password string
	upsName  string

	// Timeout bounds each network operation. Zero means DefaultTimeout.
	Timeout time.Duration

	conn   net.Conn
	reader *bufio.Reader
}

// NewClient dials upsd, authenticates if a username is configured, and returns
// a ready Client. The caller must Close it.
func NewClient(host string, port int, username, password, upsName string) (*Client, error) {
	c := &Client{
		host:     host,
		port:     port,
		username: username,
		password: password,
		upsName:  upsName,
	}
	if err := c.connect(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Client) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return DefaultTimeout
}

// connect establishes a fresh connection, discarding any previous one.
func (c *Client) connect() error {
	// Close first. Replacing the connection without closing it is precisely
	// the leak this client exists to fix.
	c.closeConn()

	addr := net.JoinHostPort(c.host, strconv.Itoa(c.port))
	conn, err := net.DialTimeout("tcp", addr, c.timeout())
	if err != nil {
		return fmt.Errorf("connecting to NUT at %s: %w", addr, err)
	}
	c.conn = conn
	c.reader = bufio.NewReader(conn)

	if c.username != "" {
		if err := c.authenticate(); err != nil {
			c.closeConn()
			return err
		}
	}
	return nil
}

func (c *Client) authenticate() error {
	if _, err := c.simpleCommand("USERNAME " + c.username); err != nil {
		return fmt.Errorf("authenticating with NUT: %w", err)
	}
	if _, err := c.simpleCommand("PASSWORD " + c.password); err != nil {
		return fmt.Errorf("authenticating with NUT: %w", err)
	}
	return nil
}

// closeConn drops the current connection. Errors are ignored: the connection
// is being discarded either way, and a failed close must not mask the error
// that prompted it.
func (c *Client) closeConn() {
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
		c.reader = nil
	}
}

// send writes one command line, with a deadline.
func (c *Client) send(cmd string) error {
	if c.conn == nil {
		return ErrNotConnected
	}
	if err := c.conn.SetDeadline(time.Now().Add(c.timeout())); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(c.conn, "%s\n", cmd); err != nil {
		return fmt.Errorf("sending %q: %w", cmd, err)
	}
	return nil
}

// readLine reads one response line, stripped of its terminator.
func (c *Client) readLine() (string, error) {
	if c.reader == nil {
		return "", ErrNotConnected
	}
	line, err := c.reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("reading response: %w", err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// simpleCommand sends a command expecting a single-line reply.
func (c *Client) simpleCommand(cmd string) (string, error) {
	if err := c.send(cmd); err != nil {
		return "", err
	}
	line, err := c.readLine()
	if err != nil {
		return "", err
	}
	if err := protocolError(line); err != nil {
		return "", err
	}
	return line, nil
}

// listCommand sends a LIST command and returns the lines between its BEGIN and
// END markers.
func (c *Client) listCommand(cmd string) ([]string, error) {
	if err := c.send(cmd); err != nil {
		return nil, err
	}

	begin := "BEGIN " + cmd
	end := "END " + cmd

	first, err := c.readLine()
	if err != nil {
		return nil, err
	}
	if err := protocolError(first); err != nil {
		return nil, err
	}
	if first != begin {
		return nil, fmt.Errorf("expected %q, got %q", begin, first)
	}

	var lines []string
	for {
		line, err := c.readLine()
		if err != nil {
			return nil, err
		}
		if line == end {
			return lines, nil
		}
		if err := protocolError(line); err != nil {
			return nil, err
		}
		lines = append(lines, line)
	}
}

// protocolError converts an "ERR ..." reply into a Go error.
func protocolError(line string) error {
	if code, ok := strings.CutPrefix(line, "ERR "); ok {
		return fmt.Errorf("upsd error: %s", code)
	}
	return nil
}

// Poll fetches the current variable set for the configured UPS. If the
// connection has gone away it re-dials once and retries, so a upsd restart or
// a dropped connection heals on the next poll rather than failing forever.
func (c *Client) Poll() ([]Variable, error) {
	if c.conn == nil {
		if err := c.connect(); err != nil {
			return nil, err
		}
	}

	vars, err := c.pollOnce()
	if err == nil {
		return vars, nil
	}

	// A failed command means the connection is unusable. Drop it and try once
	// more on a fresh one; if that fails too, the fault is upstream.
	c.closeConn()
	if cerr := c.connect(); cerr != nil {
		return nil, fmt.Errorf("%w (reconnect also failed: %v)", err, cerr)
	}
	return c.pollOnce()
}

func (c *Client) pollOnce() ([]Variable, error) {
	lines, err := c.listCommand("LIST VAR " + c.upsName)
	if err != nil {
		return nil, fmt.Errorf("listing variables for %q: %w", c.upsName, err)
	}

	vars := make([]Variable, 0, len(lines))
	for _, line := range lines {
		v, err := parseVarLine(line, c.upsName)
		if err != nil {
			return nil, fmt.Errorf("parsing %q: %w", line, err)
		}
		vars = append(vars, v)
	}
	return vars, nil
}

// parseVarLine parses a single upsd variable line:
//
//	VAR <upsname> <varname> "<value>"
//
// Values are quoted and may contain spaces; upsd escapes a literal " or \
// with a backslash.
func parseVarLine(line, upsName string) (Variable, error) {
	rest, ok := strings.CutPrefix(line, "VAR ")
	if !ok {
		return Variable{}, errors.New(`line does not start with "VAR "`)
	}

	gotUPS, rest, ok := strings.Cut(rest, " ")
	if !ok {
		return Variable{}, errors.New("missing variable name")
	}
	if gotUPS != upsName {
		return Variable{}, fmt.Errorf("line is for UPS %q, want %q", gotUPS, upsName)
	}

	name, quoted, ok := strings.Cut(rest, " ")
	if !ok {
		return Variable{}, errors.New("missing value")
	}
	if name == "" {
		return Variable{}, errors.New("empty variable name")
	}

	value, err := unquote(quoted)
	if err != nil {
		return Variable{}, err
	}
	return Variable{Name: name, Value: value}, nil
}

// unquote strips the surrounding quotes from a upsd value and resolves its
// backslash escapes.
func unquote(s string) (string, error) {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return "", errors.New("value is not quoted")
	}
	body := s[1 : len(s)-1]

	if !strings.ContainsRune(body, '\\') {
		return body, nil
	}
	var b strings.Builder
	b.Grow(len(body))
	for i := 0; i < len(body); i++ {
		if body[i] == '\\' && i+1 < len(body) {
			i++
		}
		b.WriteByte(body[i])
	}
	return b.String(), nil
}

// Close logs out politely and closes the connection. The LOGOUT is best
// effort — the socket is closed regardless, which is the part that matters.
func (c *Client) Close() error {
	if c.conn == nil {
		return nil
	}
	_, logoutErr := c.simpleCommand("LOGOUT")
	c.closeConn()
	if logoutErr != nil {
		// Not worth surfacing: the connection is gone either way, and a upsd
		// that has already vanished is the common case.
		return nil
	}
	return nil
}
