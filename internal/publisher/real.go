package publisher

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/sweeney/ups-mqtt/internal/config"
)

// DefaultPublishTimeout bounds how long a single publish may block.
//
// paho's token.Wait() has no deadline.  While the client is disconnected and
// auto-reconnecting, a publish never completes, so an unbounded wait stops the
// poll loop outright — a flaky link becomes total silence rather than a late
// message.  That risk is negligible for a broker on the same switch and real
// for one across a WireGuard tunnel.
//
// It is deliberately shorter than any sensible poll interval: a poll that
// cannot finish before the next one begins is of no use to anyone.
const DefaultPublishTimeout = 5 * time.Second

// MQTTPublisher wraps paho.mqtt.golang and implements Publisher.
type MQTTPublisher struct {
	client  mqtt.Client
	qos     byte
	timeout time.Duration
}

// NewMQTTPublisher creates a connected MQTT client.
// lwtTopic and lwtPayload are used for the Last Will and Testament message,
// published by the broker if the client disconnects unexpectedly.
func NewMQTTPublisher(cfg config.MQTTConfig, lwtTopic, lwtPayload string) (*MQTTPublisher, error) {
	opts := mqtt.NewClientOptions()
	opts.AddBroker(cfg.Broker)
	opts.SetClientID(cfg.ClientID)
	if cfg.Username != "" {
		opts.SetUsername(cfg.Username)
		opts.SetPassword(cfg.Password)
	}
	// The broker declares a client dead — and fires the LWT — after roughly
	// 1.5x the keep-alive with no traffic.  This is the only signal that
	// survives an outage which also takes out the uplink, so it is kept short:
	// ~45 s to notice rather than ~90 s, for one extra PINGREQ a minute.
	opts.SetKeepAlive(30 * time.Second)
	opts.SetCleanSession(true)
	opts.SetAutoReconnect(true)
	opts.SetWill(lwtTopic, lwtPayload, cfg.QOS, true)

	if cfg.TLSCACert != "" {
		tlsCfg, err := newTLSConfig(cfg.TLSCACert)
		if err != nil {
			return nil, fmt.Errorf("loading TLS CA cert %q: %w", cfg.TLSCACert, err)
		}
		opts.SetTLSConfig(tlsCfg)
	}

	client := mqtt.NewClient(opts)
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		return nil, fmt.Errorf("connecting to MQTT broker %q: %w", cfg.Broker, token.Error())
	}
	return &MQTTPublisher{client: client, qos: cfg.QOS, timeout: DefaultPublishTimeout}, nil
}

// Publish sends a single MQTT message and waits for the broker to acknowledge,
// up to the publisher's timeout.
//
// A disconnected client fails immediately rather than waiting out the timeout:
// with ~50 messages per poll, waiting on each in turn would stall the loop for
// minutes.  Nothing is lost by giving up — the session is a clean one, so
// paho would not durably queue the message anyway, and the next poll after
// reconnection publishes current state, which is what a subscriber wants.
func (p *MQTTPublisher) Publish(msg Message) error {
	if !p.client.IsConnected() {
		return fmt.Errorf("publishing to %q: not connected to broker", msg.Topic)
	}
	token := p.client.Publish(msg.Topic, p.qos, msg.Retained, msg.Payload)
	if !token.WaitTimeout(p.timeout) {
		return fmt.Errorf("publishing to %q: timed out after %s", msg.Topic, p.timeout)
	}
	return token.Error()
}

// Close disconnects from the broker gracefully.
func (p *MQTTPublisher) Close() error {
	p.client.Disconnect(250)
	return nil
}

// newTLSConfig builds a *tls.Config that trusts caFile as an additional CA.
func newTLSConfig(caFile string) (*tls.Config, error) {
	caCert, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("reading CA cert: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("failed to parse CA cert from %q", caFile)
	}
	return &tls.Config{RootCAs: pool}, nil
}
