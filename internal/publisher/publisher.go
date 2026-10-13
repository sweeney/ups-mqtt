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
	"github.com/sweeney/ups-mqtt/internal/outages"
)

// Message is a single MQTT publish request.
type Message struct {
	Topic    string
	Payload  string
	Retained bool
	// QOS is chosen per message, not per client: telemetry and events are
	// published at different levels.  See PublishConfig.
	QOS byte
}

// Publisher is the minimal interface the rest of the codebase uses to send
// MQTT messages. The real MQTT client and FakePublisher both implement it.
type Publisher interface {
	Publish(msg Message) error
	Close() error
}

// PublishConfig groups the MQTT routing parameters so callers don't need to
// thread separate arguments through every function.
//
// Telemetry and events go out at separate QoS levels.  Behind a local broker
// bridged to a remote one, the bridge queues QoS 1 messages while its link is
// down, up to a cap, and drops the newest once the cap is reached.  Telemetry
// at QoS 1 would fill that queue with readings the next poll supersedes, and
// the outage it exists to report would be the message dropped.
type PublishConfig struct {
	Prefix   string
	UPSName  string
	Retained bool
	// TelemetryQOS is used for state and computed/* — superseded every poll.
	TelemetryQOS byte
	// EventQOS is used for the outage topic, its clear, and the offline
	// announcement — things that happen once and must not be lost.
	EventQOS byte
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
	// ClockSynced says whether outage_started_at can be trusted.  On a host
	// without an RTC the wall clock is wrong after a power-loss reboot until
	// NTP syncs.  Absent where sync cannot be determined.
	ClockSynced *bool `json:"clock_synced,omitempty"`
}

// OutageTopic returns the MQTT topic used for the outage message.
func OutageTopic(prefix, upsName string) string {
	return fmt.Sprintf("%s/%s/outage", prefix, upsName)
}

// PublishOutage marshals and publishes an OutageMessage.  outageStart is when
// the OB condition was first detected, possibly by a previous process; it is
// used to compute outage_duration_secs and is independent of the current poll
// time.  clockSynced says whether outageStart can be trusted (nil: unknown).
func PublishOutage(
	vars map[string]string,
	m metrics.Metrics,
	outageStart time.Time,
	clockSynced *bool,
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
		ClockSynced:          clockSynced,
	}

	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshalling outage: %w", err)
	}
	return pub.Publish(Message{
		Topic:    OutageTopic(cfg.Prefix, cfg.UPSName),
		Payload:  string(payload),
		Retained: true,
		QOS:      cfg.EventQOS,
	})
}

// ClearOutage publishes an empty retained payload to the outage topic, which
// clears any previously retained outage message from the broker.
func ClearOutage(cfg PublishConfig, pub Publisher) error {
	return pub.Publish(Message{
		Topic:    OutageTopic(cfg.Prefix, cfg.UPSName),
		Payload:  "",
		Retained: true,
		QOS:      cfg.EventQOS,
	})
}

// LastOutageMessage is published, retained, to {prefix}/{label}/last_outage
// when an outage ends.  Unlike the outage topic, it is never cleared: it is
// the record a subscriber that was not watching at the time can read
// afterwards.  Each outage replaces the previous one.
type LastOutageMessage struct {
	UPSName             string   `json:"ups_name"`
	StartedAt           string   `json:"started_at"`
	EndedAt             string   `json:"ended_at"`
	DurationSecs        int64    `json:"duration_secs"`
	LowBatteryReached   bool     `json:"low_battery_reached"`
	MinBatteryChargePct *float64 `json:"min_battery_charge_pct,omitempty"`
	ClockSynced         *bool    `json:"clock_synced,omitempty"`
	SpansRestart        bool     `json:"spans_restart"`
}

// LastOutageTopic returns the MQTT topic used for the last_outage summary.
func LastOutageTopic(prefix, upsName string) string {
	return fmt.Sprintf("%s/%s/last_outage", prefix, upsName)
}

// PublishLastOutage publishes the summary of an outage that has ended.  It is
// an event: always retained, at the event QoS.
func PublishLastOutage(s outages.Summary, cfg PublishConfig, pub Publisher) error {
	payload, err := json.Marshal(LastOutageMessage{
		UPSName:             cfg.UPSName,
		StartedAt:           s.StartedAt.UTC().Format(time.RFC3339),
		EndedAt:             s.EndedAt.UTC().Format(time.RFC3339),
		DurationSecs:        int64(s.Duration().Seconds()),
		LowBatteryReached:   s.LowBatteryReached,
		MinBatteryChargePct: s.MinBatteryChargePct,
		ClockSynced:         s.ClockSynced,
		SpansRestart:        s.SpansRestart,
	})
	if err != nil {
		return fmt.Errorf("marshalling last_outage: %w", err)
	}
	return pub.Publish(Message{
		Topic:    LastOutageTopic(cfg.Prefix, cfg.UPSName),
		Payload:  string(payload),
		Retained: true,
		QOS:      cfg.EventQOS,
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

// noisyVariableFamilies are variable prefixes excluded from the published
// state.  They describe NUT itself, or hardware the device does not have, and
// are re-sent unchanged on every poll.
//
//   - "driver." is NUT's own build and configuration metadata: driver.version,
//     driver.parameter.* (our own ups.conf echoed back), driver.debug.  It
//     says nothing about the UPS.  driver.state is also the single noisiest
//     value in the system — 165 of the 236 value changes observed across a
//     full outage capture, flapping between "updateinfo" and "quiet".
//   - "output.L2." and "output.L3." are phantom phases.  Every UPS here is
//     single-phase, so they are permanently zero.
//
// This is a denylist, not an allowlist, so variables from an unfamiliar UPS
// still reach subscribers: the three devices in use already expose three
// different variable sets, and an allowlist would silently swallow anything
// unanticipated.
var noisyVariableFamilies = []string{
	"driver.",
	"output.L2.",
	"output.L3.",
}

// publishableVariables returns vars with the noisy families removed.  The
// input map is never modified.
func publishableVariables(vars map[string]string) map[string]string {
	out := make(map[string]string, len(vars))
	for name, value := range vars {
		if isNoisyVariable(name) {
			continue
		}
		out[name] = value
	}
	return out
}

// isNoisyVariable reports whether name belongs to a denied family.
func isNoisyVariable(name string) bool {
	for _, prefix := range noisyVariableFamilies {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// PublishAll publishes every computed metric under the "computed/" sub-tree,
// plus the combined JSON state topic.
//
// Raw NUT variables are deliberately NOT published as individual topics.  They
// duplicated, value for value, what the state topic already carries as JSON —
// around 46 retained topics per poll that no subscriber read.  The state topic
// is published on every poll regardless of whether anything changed, so it
// doubles as a heartbeat.
//
// Every message is attempted even when earlier ones fail, and the failures are
// returned joined.  Stopping at the first error would let one unlucky topic
// suppress every message after it — including the state topic, which is also
// the LWT topic.
func PublishAll(
	vars map[string]string,
	m metrics.Metrics,
	cfg PublishConfig,
	pub Publisher,
) error {
	var errs []error

	// --- computed metric topics ---
	for name, payload := range m.AsTopicMap() {
		topic := fmt.Sprintf("%s/%s/computed/%s", cfg.Prefix, cfg.UPSName, name)
		if err := pub.Publish(Message{Topic: topic, Payload: payload, Retained: cfg.Retained, QOS: cfg.TelemetryQOS}); err != nil {
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
		Variables: publishableVariables(vars),
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
		QOS:      cfg.TelemetryQOS,
	})
}
