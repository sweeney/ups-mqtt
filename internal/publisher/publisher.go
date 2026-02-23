// Package publisher handles MQTT topic routing and JSON state assembly.
package publisher

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/sweeney/ups-mqtt/internal/metrics"
)

// Message is a single MQTT publish request.
type Message struct {
	Topic    string
	Payload  string
	Retained bool
}

// Publisher is the minimal interface the rest of the codebase uses to send
// MQTT messages. The real MQTT client and FakePublisher both implement it.
type Publisher interface {
	Publish(msg Message) error
	Close() error
}

// PublishConfig groups the MQTT routing parameters so callers don't need to
// thread three separate arguments through every function.
type PublishConfig struct {
	Prefix   string
	UPSName  string
	Retained bool
}

// StateMessage is the JSON payload for the combined state topic.
// Computed uses metrics.Metrics directly — its JSON tags define the wire format.
type StateMessage struct {
	Timestamp string            `json:"timestamp"`
	UPSName   string            `json:"ups_name"`
	Variables map[string]string `json:"variables"`
	Computed  metrics.Metrics   `json:"computed"`
}

// OnlineState is the LWT / online-announcement payload.
type OnlineState struct {
	Online    bool   `json:"online"`
	Timestamp string `json:"timestamp"`
}

// PublishAll publishes every NUT variable as an individual topic, every
// computed metric under the "computed/" sub-tree, and the combined JSON
// state topic.  It returns the first publish error encountered.
func PublishAll(
	vars map[string]string,
	m metrics.Metrics,
	cfg PublishConfig,
	pub Publisher,
) error {
	// --- individual NUT variable topics ---
	for name, value := range vars {
		topic := fmt.Sprintf("%s/%s/%s", cfg.Prefix, cfg.UPSName, strings.ReplaceAll(name, ".", "/"))
		if err := pub.Publish(Message{Topic: topic, Payload: value, Retained: cfg.Retained}); err != nil {
			return err
		}
	}

	// --- computed metric topics ---
	for name, payload := range m.AsTopicMap() {
		topic := fmt.Sprintf("%s/%s/computed/%s", cfg.Prefix, cfg.UPSName, name)
		if err := pub.Publish(Message{Topic: topic, Payload: payload, Retained: cfg.Retained}); err != nil {
			return err
		}
	}

	// --- combined JSON state topic ---
	return publishState(vars, m, cfg, pub)
}

// FormatOffline returns the JSON payload for the offline announcement.
func FormatOffline() string {
	payload, _ := json.Marshal(OnlineState{
		Online:    false,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})
	return string(payload)
}

// StateTopic returns the MQTT topic used for the combined state message.
func StateTopic(prefix, upsName string) string {
	return fmt.Sprintf("%s/%s/state", prefix, upsName)
}

// publishState marshals and publishes the combined JSON state message.
func publishState(
	vars map[string]string,
	m metrics.Metrics,
	cfg PublishConfig,
	pub Publisher,
) error {
	state := StateMessage{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		UPSName:   cfg.UPSName,
		Variables: vars,
		Computed:  m,
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshalling state: %w", err)
	}
	return pub.Publish(Message{
		Topic:    StateTopic(cfg.Prefix, cfg.UPSName),
		Payload:  string(payload),
		Retained: cfg.Retained,
	})
}
