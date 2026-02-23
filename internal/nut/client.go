package nut

import (
	"fmt"

	gonut "github.com/robbiet480/go.nut"
)

// Client connects to a NUT upsd daemon and implements Poller.
// On Poll error the connection is marked stale; the next Poll reconnects
// automatically before fetching variables.
type Client struct {
	host     string
	port     int
	username string
	password string
	upsName  string
	conn     *gonut.Client
	stale    bool
}

// NewClient dials upsd and returns a ready Client, or an error if the
// initial connection fails.
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

func (c *Client) connect() error {
	conn, err := gonut.Connect(c.host, c.port)
	if err != nil {
		return fmt.Errorf("connecting to NUT at %s:%d: %w", c.host, c.port, err)
	}
	if c.username != "" {
		if _, err := conn.Authenticate(c.username, c.password); err != nil {
			_, _ = conn.Disconnect()
			return fmt.Errorf("authenticating with NUT: %w", err)
		}
	}
	c.conn = &conn
	c.stale = false
	return nil
}

// Poll fetches the current variable set from the configured UPS.
// If the connection is stale it reconnects first.
func (c *Client) Poll() ([]Variable, error) {
	if c.stale {
		if err := c.connect(); err != nil {
			return nil, err
		}
	}

	upsList, err := c.conn.GetUPSList()
	if err != nil {
		c.stale = true
		return nil, fmt.Errorf("listing UPS: %w", err)
	}

	var target *gonut.UPS
	for i := range upsList {
		if upsList[i].Name == c.upsName {
			target = &upsList[i]
			break
		}
	}
	if target == nil {
		return nil, fmt.Errorf("UPS %q not found in upsd", c.upsName)
	}

	nutVars, err := target.GetVariables()
	if err != nil {
		c.stale = true
		return nil, fmt.Errorf("getting variables for %q: %w", c.upsName, err)
	}

	vars := make([]Variable, len(nutVars))
	for i, v := range nutVars {
		vars[i] = Variable{
			Name:  v.Name,
			Value: fmt.Sprintf("%v", v.Value),
		}
	}
	return vars, nil
}

// Close disconnects from upsd.
func (c *Client) Close() error {
	if c.conn != nil {
		_, err := c.conn.Disconnect()
		c.conn = nil
		return err
	}
	return nil
}
