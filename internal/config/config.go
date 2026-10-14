// Package config loads and merges configuration from a TOML file and
// environment variable overrides.
package config

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Duration wraps time.Duration so that BurntSushi/toml can decode "30s"-style
// strings via the encoding.TextUnmarshaler interface.
type Duration struct {
	time.Duration
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (d *Duration) UnmarshalText(text []byte) error {
	dur, err := time.ParseDuration(string(text))
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", string(text), err)
	}
	d.Duration = dur
	return nil
}

// NUTConfig holds Network UPS Tools client settings.
type NUTConfig struct {
	Host         string   `toml:"host"`
	Port         int      `toml:"port"`
	Username     string   `toml:"username"`
	Password     string   `toml:"password"`
	UPSName      string   `toml:"ups_name"`
	Label        string   `toml:"label"`
	PollInterval Duration `toml:"poll_interval"`

	// SampleInterval is how often the UPS is read between publishes, so a
	// change of power state is seen — and published — within a sample
	// rather than a whole poll.  Publishing still follows PollInterval.
	// Zero means DefaultSampleInterval; either way it is capped at
	// PollInterval.
	SampleInterval Duration `toml:"sample_interval"`
}

// DefaultSampleInterval is the sample interval when none is configured.  A
// sample is one LIST VAR round trip to upsd, normally on the same host.
const DefaultSampleInterval = 2 * time.Second

// EffectiveSampleInterval returns SampleInterval, or DefaultSampleInterval
// when unset, capped at PollInterval.
func (c NUTConfig) EffectiveSampleInterval() time.Duration {
	d := c.SampleInterval.Duration
	if d <= 0 {
		d = DefaultSampleInterval
	}
	if c.PollInterval.Duration > 0 && d > c.PollInterval.Duration {
		d = c.PollInterval.Duration
	}
	return d
}

// EffectiveLabel returns Label if set, otherwise UPSName.
// Use this for MQTT topic routing; use UPSName only for NUT device lookup.
func (c NUTConfig) EffectiveLabel() string {
	if c.Label != "" {
		return c.Label
	}
	return c.UPSName
}

// MQTTConfig holds MQTT broker connection settings.
type MQTTConfig struct {
	Broker      string `toml:"broker"`
	Username    string `toml:"username"`
	Password    string `toml:"password"`
	ClientID    string `toml:"client_id"`
	TopicPrefix string `toml:"topic_prefix"`
	Retained    bool   `toml:"retained"`
	QOS         byte   `toml:"qos"`
	TLSCACert   string `toml:"tls_ca_cert"`

	// TelemetryQOS and EventQOS split QOS by message kind: telemetry (state,
	// computed/*) is superseded every poll, events (outage, its clear,
	// last_outage, the offline announcement and the LWT) are not.  Each falls
	// back to QOS when unset; they are pointers so that an explicit 0 still
	// counts as set.
	TelemetryQOS *byte `toml:"telemetry_qos"`
	EventQOS     *byte `toml:"event_qos"`
}

// EffectiveTelemetryQOS returns TelemetryQOS if set, otherwise QOS.
func (c MQTTConfig) EffectiveTelemetryQOS() byte {
	if c.TelemetryQOS != nil {
		return *c.TelemetryQOS
	}
	return c.QOS
}

// EffectiveEventQOS returns EventQOS if set, otherwise QOS.
func (c MQTTConfig) EffectiveEventQOS() byte {
	if c.EventQOS != nil {
		return *c.EventQOS
	}
	return c.QOS
}

// Config is the top-level configuration struct.
type Config struct {
	// StateDir is where an outage in progress is persisted, so it survives a
	// restart or power loss.  Under systemd or launchd it can be left empty:
	// $STATE_DIRECTORY (from StateDirectory= in the unit, or the macOS
	// plist's EnvironmentVariables) is the fallback.
	// See EffectiveStateDir.
	StateDir string     `toml:"state_dir"`
	NUT      NUTConfig  `toml:"nut"`
	MQTT     MQTTConfig `toml:"mqtt"`
}

// EffectiveStateDir returns the directory for persistent state, and where
// that came from for the log.  An explicit StateDir (state_dir, or
// UPS_MQTT_STATE_DIR) wins, as explicit settings do everywhere else; then the
// first entry of $STATE_DIRECTORY, systemd's default from StateDirectory=.
// Empty means no persistence.
func (c *Config) EffectiveStateDir() (dir, source string) {
	if c.StateDir != "" {
		return c.StateDir, "state_dir"
	}
	if v := os.Getenv("STATE_DIRECTORY"); v != "" {
		// systemd joins several directories with ':'.
		return strings.SplitN(v, ":", 2)[0], "$STATE_DIRECTORY"
	}
	return "", ""
}

// Load reads config from the first existing path in paths, then applies
// environment variable overrides.  Missing files are skipped silently;
// a malformed file returns an error.  Calling Load() with no arguments
// returns pure defaults plus any env overrides.
func Load(paths ...string) (*Config, error) {
	cfg := defaults()

	for _, path := range paths {
		if path == "" {
			continue
		}
		if _, statErr := os.Stat(path); statErr == nil {
			if _, err := toml.DecodeFile(path, cfg); err != nil {
				return nil, fmt.Errorf("parsing config %q: %w", path, err)
			}
			break // first found file wins
		} else if !os.IsNotExist(statErr) {
			return nil, fmt.Errorf("checking config path %q: %w", path, statErr)
		}
	}

	applyEnvOverrides(cfg)
	if err := cfg.MQTT.validateQOS(); err != nil {
		return nil, err
	}
	if err := cfg.NUT.validateIntervals(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// validateIntervals rejects a poll interval that is not positive.  Zero once
// panicked at startup; with sampling it would instead publish on every
// sample, five times the intended rate, without a word.
func (c NUTConfig) validateIntervals() error {
	if c.PollInterval.Duration <= 0 {
		return fmt.Errorf("nut.poll_interval = %s: must be positive", c.PollInterval.Duration)
	}
	if c.SampleInterval.Duration < 0 {
		return fmt.Errorf("nut.sample_interval = %s: must not be negative", c.SampleInterval.Duration)
	}
	return nil
}

// validateQOS rejects QoS levels above 2.  Nothing downstream checks: paho
// puts the value straight into PUBLISH and, for the LWT, into CONNECT, where
// the broker rejects it.  The connect retry would then repeat that refusal
// for ever, looking like a network fault, so it has to fail here, at load.
func (c MQTTConfig) validateQOS() error {
	for _, q := range []struct {
		key   string
		value *byte
	}{
		{"qos", &c.QOS},
		{"telemetry_qos", c.TelemetryQOS},
		{"event_qos", c.EventQOS},
	} {
		if q.value != nil && *q.value > 2 {
			return fmt.Errorf("mqtt.%s = %d: QoS must be 0, 1 or 2", q.key, *q.value)
		}
	}
	return nil
}

func defaults() *Config {
	return &Config{
		NUT: NUTConfig{
			Host:         "localhost",
			Port:         3493,
			UPSName:      "cyberpower",
			PollInterval: Duration{30 * time.Second},
		},
		MQTT: MQTTConfig{
			Broker:      "tcp://localhost:1883",
			ClientID:    "ups-mqtt",
			TopicPrefix: "ups",
			Retained:    true,
			QOS:         1,
		},
	}
}

// applyEnvOverrides copies any set UPS_MQTT_* environment variables into cfg.
func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("UPS_MQTT_STATE_DIR"); v != "" {
		cfg.StateDir = v
	}
	if v := os.Getenv("UPS_MQTT_NUT_HOST"); v != "" {
		cfg.NUT.Host = v
	}
	if v := os.Getenv("UPS_MQTT_NUT_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			cfg.NUT.Port = p
		} else {
			log.Printf("config: ignoring invalid UPS_MQTT_NUT_PORT=%q: %v", v, err)
		}
	}
	if v := os.Getenv("UPS_MQTT_NUT_USERNAME"); v != "" {
		cfg.NUT.Username = v
	}
	if v := os.Getenv("UPS_MQTT_NUT_PASSWORD"); v != "" {
		cfg.NUT.Password = v
	}
	if v := os.Getenv("UPS_MQTT_NUT_UPS_NAME"); v != "" {
		cfg.NUT.UPSName = v
	}
	if v := os.Getenv("UPS_MQTT_NUT_LABEL"); v != "" {
		cfg.NUT.Label = v
	}
	if v := os.Getenv("UPS_MQTT_NUT_POLL_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.NUT.PollInterval = Duration{d}
		} else {
			log.Printf("config: ignoring invalid UPS_MQTT_NUT_POLL_INTERVAL=%q: %v", v, err)
		}
	}
	if v := os.Getenv("UPS_MQTT_NUT_SAMPLE_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.NUT.SampleInterval = Duration{d}
		} else {
			log.Printf("config: ignoring invalid UPS_MQTT_NUT_SAMPLE_INTERVAL=%q: %v", v, err)
		}
	}
	if v := os.Getenv("UPS_MQTT_MQTT_BROKER"); v != "" {
		cfg.MQTT.Broker = v
	}
	if v := os.Getenv("UPS_MQTT_MQTT_USERNAME"); v != "" {
		cfg.MQTT.Username = v
	}
	if v := os.Getenv("UPS_MQTT_MQTT_PASSWORD"); v != "" {
		cfg.MQTT.Password = v
	}
	if v := os.Getenv("UPS_MQTT_MQTT_CLIENT_ID"); v != "" {
		cfg.MQTT.ClientID = v
	}
	if v := os.Getenv("UPS_MQTT_MQTT_TOPIC_PREFIX"); v != "" {
		cfg.MQTT.TopicPrefix = v
	}
	if v := os.Getenv("UPS_MQTT_MQTT_RETAINED"); v != "" {
		cfg.MQTT.Retained = v == "true" || v == "1"
	}
	if q, ok := envQOS("UPS_MQTT_MQTT_QOS"); ok {
		cfg.MQTT.QOS = q
	}
	if q, ok := envQOS("UPS_MQTT_MQTT_TELEMETRY_QOS"); ok {
		cfg.MQTT.TelemetryQOS = &q
	}
	if q, ok := envQOS("UPS_MQTT_MQTT_EVENT_QOS"); ok {
		cfg.MQTT.EventQOS = &q
	}
	if v := os.Getenv("UPS_MQTT_MQTT_TLS_CA_CERT"); v != "" {
		cfg.MQTT.TLSCACert = v
	}
}

// envQOS reads a QoS level from the named environment variable.  ok is false
// when the variable is unset or invalid; an invalid value is logged.
func envQOS(name string) (byte, bool) {
	v := os.Getenv(name)
	if v == "" {
		return 0, false
	}
	q, err := strconv.ParseUint(v, 10, 8)
	if err != nil {
		log.Printf("config: ignoring invalid %s=%q: %v", name, v, err)
		return 0, false
	}
	return byte(q), true
}
