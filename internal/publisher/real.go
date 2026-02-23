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

// MQTTPublisher wraps paho.mqtt.golang and implements Publisher.
type MQTTPublisher struct {
	client mqtt.Client
	qos    byte
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
	opts.SetKeepAlive(60 * time.Second)
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
	return &MQTTPublisher{client: client, qos: cfg.QOS}, nil
}

// Publish sends a single MQTT message and waits for the broker to acknowledge.
func (p *MQTTPublisher) Publish(msg Message) error {
	token := p.client.Publish(msg.Topic, p.qos, msg.Retained, msg.Payload)
	token.Wait()
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
