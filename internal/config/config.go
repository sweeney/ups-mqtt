// Package config loads and merges configuration from a TOML file and
// environment variable overrides.
package config

import (
	"fmt"
	"log"
	"os"
	"strconv"
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
	PollInterval Duration `toml:"poll_interval"`
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
}

// Config is the top-level configuration struct.
type Config struct {
	NUT  NUTConfig  `toml:"nut"`
	MQTT MQTTConfig `toml:"mqtt"`
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
	return cfg, nil
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
	if v := os.Getenv("UPS_MQTT_NUT_POLL_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.NUT.PollInterval = Duration{d}
		} else {
			log.Printf("config: ignoring invalid UPS_MQTT_NUT_POLL_INTERVAL=%q: %v", v, err)
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
	if v := os.Getenv("UPS_MQTT_MQTT_QOS"); v != "" {
		if q, err := strconv.ParseUint(v, 10, 8); err == nil {
			cfg.MQTT.QOS = byte(q)
		} else {
			log.Printf("config: ignoring invalid UPS_MQTT_MQTT_QOS=%q: %v", v, err)
		}
	}
	if v := os.Getenv("UPS_MQTT_MQTT_TLS_CA_CERT"); v != "" {
		cfg.MQTT.TLSCACert = v
	}
}
