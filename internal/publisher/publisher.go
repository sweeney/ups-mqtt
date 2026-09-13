// Package publisher handles MQTT topic routing and JSON state assembly.
package publisher

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
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

// OutageMessage is published to {prefix}/{ups_name}/outage whenever the UPS is
// running on battery.  It is always retained so late subscribers receive it,
// and cleared (empty retained payload) when mains power is restored.
// Fields the UPS cannot supply are omitted rather than sent as zero.  This
// matters most for EstimatedDepletionAt: with an unknown runtime the old
// behaviour dated it to the current instant, announcing mid-outage that the
// battery was about to die.  Silence is the correct answer there.
type OutageMessage struct {
	Timestamp            string   `json:"timestamp"`
	UPSName              string   `json:"ups_name"`
	OutageStartedAt      string   `json:"outage_started_at"`
	OutageDurationSecs   int64    `json:"outage_duration_secs"`
	Status               string   `json:"status"`
	StatusDisplay        string   `json:"status_display"`
	BatteryChargePct     *float64 `json:"battery_charge_pct,omitempty"`
	BatteryVoltage       *float64 `json:"battery_voltage,omitempty"`
	BatteryRuntimeSecs   *float64 `json:"battery_runtime_secs,omitempty"`
	BatteryRuntimeMins   *float64 `json:"battery_runtime_mins,omitempty"`
	EstimatedDepletionAt string   `json:"estimated_depletion_at,omitempty"`
	LoadWatts            *float64 `json:"load_watts,omitempty"`
	LowBattery           bool     `json:"low_battery"`
}

// OutageTopic returns the MQTT topic used for the outage message.
func OutageTopic(prefix, upsName string) string {
	return fmt.Sprintf("%s/%s/outage", prefix, upsName)
}

// PublishOutage marshals and publishes an OutageMessage.  outageStart is when
// the OB condition was first detected this session; it is used to compute
// outage_duration_secs and is independent of the current poll time.
func PublishOutage(
	vars map[string]string,
	m metrics.Metrics,
	outageStart time.Time,
	cfg PublishConfig,
	pub Publisher,
) error {
	now := time.Now().UTC()

	runtimeSecs := optionalVar(vars, "battery.runtime")
	chargePct := optionalVar(vars, "battery.charge")

	// Only date a depletion estimate when the UPS actually reports runtime.
	var depletionAt string
	if runtimeSecs != nil {
		depletionAt = now.Add(time.Duration(*runtimeSecs) * time.Second).Format(time.RFC3339)
	}

	msg := OutageMessage{
		Timestamp:            now.Format(time.RFC3339),
		UPSName:              cfg.UPSName,
		OutageStartedAt:      outageStart.UTC().Format(time.RFC3339),
		OutageDurationSecs:   int64(now.Sub(outageStart).Seconds()),
		Status:               vars["ups.status"],
		StatusDisplay:        m.StatusDisplay,
		BatteryChargePct:     chargePct,
		BatteryVoltage:       optionalVar(vars, "battery.voltage"),
		BatteryRuntimeSecs:   runtimeSecs,
		BatteryRuntimeMins:   m.BatteryRuntimeMins,
		EstimatedDepletionAt: depletionAt,
		LoadWatts:            m.LoadWatts,
		LowBattery:           m.LowBattery,
	}

	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshalling outage: %w", err)
	}
	return pub.Publish(Message{
		Topic:    OutageTopic(cfg.Prefix, cfg.UPSName),
		Payload:  string(payload),
		Retained: true,
	})
}

// ClearOutage publishes an empty retained payload to the outage topic, which
// clears any previously retained outage message from the broker.
func ClearOutage(cfg PublishConfig, pub Publisher) error {
	return pub.Publish(Message{
		Topic:    OutageTopic(cfg.Prefix, cfg.UPSName),
		Payload:  "",
		Retained: true,
	})
}

// optionalVar parses a NUT variable into a float64 pointer, yielding nil when
// the variable is absent or unparseable so the caller can omit it entirely.
func optionalVar(vars map[string]string, name string) *float64 {
	v, err := strconv.ParseFloat(vars[name], 64)
	if err != nil {
		return nil
	}
	return &v
}

// PublishAll publishes every NUT variable as an individual topic, every
// computed metric under the "computed/" sub-tree, and the combined JSON
// state topic.
//
// Every message is attempted even when earlier ones fail, and the failures are
// returned joined.  Stopping at the first error would let one unlucky variable
// topic suppress every message after it — including the combined state topic,
// which is also the LWT topic.  Go randomises map iteration order, so which
// topics were lost would vary from poll to poll.
func PublishAll(
	vars map[string]string,
	m metrics.Metrics,
	cfg PublishConfig,
	pub Publisher,
) error {
	var errs []error

	// --- individual NUT variable topics ---
	for name, value := range vars {
		topic := fmt.Sprintf("%s/%s/%s", cfg.Prefix, cfg.UPSName, strings.ReplaceAll(name, ".", "/"))
		if err := pub.Publish(Message{Topic: topic, Payload: value, Retained: cfg.Retained}); err != nil {
			errs = append(errs, fmt.Errorf("publishing %q: %w", topic, err))
		}
	}

	// --- computed metric topics ---
	for name, payload := range m.AsTopicMap() {
		topic := fmt.Sprintf("%s/%s/computed/%s", cfg.Prefix, cfg.UPSName, name)
		if err := pub.Publish(Message{Topic: topic, Payload: payload, Retained: cfg.Retained}); err != nil {
			errs = append(errs, fmt.Errorf("publishing %q: %w", topic, err))
		}
	}

	// --- combined JSON state topic ---
	if err := publishState(vars, m, cfg, pub); err != nil {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
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
