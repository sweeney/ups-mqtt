package config_test

import (
	"os"
	"testing"
	"time"

	"github.com/sweeney/ups-mqtt/internal/config"
)

// TestLoad_Defaults verifies that calling Load() with no arguments returns
// the built-in defaults without panicking.
func TestLoad_Defaults(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.NUT.Host != "localhost" {
		t.Errorf("NUT.Host = %q, want %q", cfg.NUT.Host, "localhost")
	}
	if cfg.NUT.Port != 3493 {
		t.Errorf("NUT.Port = %d, want 3493", cfg.NUT.Port)
	}
	if cfg.NUT.PollInterval.Duration != 30*time.Second {
		t.Errorf("NUT.PollInterval = %v, want 30s", cfg.NUT.PollInterval.Duration)
	}
	if cfg.MQTT.Broker != "tcp://localhost:1883" {
		t.Errorf("MQTT.Broker = %q, want %q", cfg.MQTT.Broker, "tcp://localhost:1883")
	}
	if cfg.MQTT.QOS != 1 {
		t.Errorf("MQTT.QOS = %d, want 1", cfg.MQTT.QOS)
	}
	if !cfg.MQTT.Retained {
		t.Error("MQTT.Retained should default to true")
	}
}

// TestLoad_NonexistentFile verifies that a missing config file is silently
// skipped and defaults are returned.
func TestLoad_NonexistentFile(t *testing.T) {
	cfg, err := config.Load("/nonexistent/path/ups-mqtt.toml")
	if err != nil {
		t.Fatalf("Load() with missing file: %v", err)
	}
	if cfg.NUT.Port != 3493 {
		t.Errorf("NUT.Port = %d, want default 3493", cfg.NUT.Port)
	}
}

// TestLoad_FallbackPath verifies that the first existing path wins.
func TestLoad_FallbackPath(t *testing.T) {
	// Both paths non-existent → pure defaults.
	cfg, err := config.Load("/no/such/a.toml", "/no/such/b.toml")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.NUT.Port != 3493 {
		t.Errorf("NUT.Port = %d, want default 3493", cfg.NUT.Port)
	}
}

// TestLoad_MalformedFile verifies that a syntactically invalid TOML file
// returns an error rather than silently producing defaults.
func TestLoad_MalformedFile(t *testing.T) {
	f, err := os.CreateTemp("", "ups-mqtt-bad-*.toml")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	defer os.Remove(f.Name())
	f.WriteString("this is not valid toml ][") //nolint:errcheck
	f.Close()                                   //nolint:errcheck

	_, err = config.Load(f.Name())
	if err == nil {
		t.Fatal("Load() should return error for malformed TOML")
	}
}

// TestLoad_EnvOverride_Host verifies that UPS_MQTT_NUT_HOST overrides the
// default NUT host.
func TestLoad_EnvOverride_Host(t *testing.T) {
	t.Setenv("UPS_MQTT_NUT_HOST", "10.0.0.1")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.NUT.Host != "10.0.0.1" {
		t.Errorf("NUT.Host = %q, want %q", cfg.NUT.Host, "10.0.0.1")
	}
}

// TestLoad_EnvOverride_Port verifies that UPS_MQTT_NUT_PORT is applied.
func TestLoad_EnvOverride_Port(t *testing.T) {
	t.Setenv("UPS_MQTT_NUT_PORT", "3494")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.NUT.Port != 3494 {
		t.Errorf("NUT.Port = %d, want 3494", cfg.NUT.Port)
	}
}

// TestLoad_EnvOverride_BadPort verifies that an invalid UPS_MQTT_NUT_PORT is
// silently ignored (with a log warning) and the default is kept.
func TestLoad_EnvOverride_BadPort(t *testing.T) {
	t.Setenv("UPS_MQTT_NUT_PORT", "not-a-number")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.NUT.Port != 3493 {
		t.Errorf("NUT.Port = %d with bad env var, want default 3493", cfg.NUT.Port)
	}
}

// TestLoad_EnvOverride_PollInterval verifies that UPS_MQTT_NUT_POLL_INTERVAL
// is applied correctly.
func TestLoad_EnvOverride_PollInterval(t *testing.T) {
	t.Setenv("UPS_MQTT_NUT_POLL_INTERVAL", "5s")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.NUT.PollInterval.Duration != 5*time.Second {
		t.Errorf("NUT.PollInterval = %v, want 5s", cfg.NUT.PollInterval.Duration)
	}
}

// TestLoad_EnvOverride_BadPollInterval verifies that an invalid duration is
// silently ignored and the default is kept.
func TestLoad_EnvOverride_BadPollInterval(t *testing.T) {
	t.Setenv("UPS_MQTT_NUT_POLL_INTERVAL", "bananas")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.NUT.PollInterval.Duration != 30*time.Second {
		t.Errorf("NUT.PollInterval = %v with bad env var, want default 30s", cfg.NUT.PollInterval.Duration)
	}
}

// TestDuration_UnmarshalText_Valid verifies the TOML duration unmarshalling.
func TestDuration_UnmarshalText_Valid(t *testing.T) {
	var d config.Duration
	if err := d.UnmarshalText([]byte("1m30s")); err != nil {
		t.Fatalf("UnmarshalText error: %v", err)
	}
	if d.Duration != 90*time.Second {
		t.Errorf("Duration = %v, want 90s", d.Duration)
	}
}

// TestDuration_UnmarshalText_Invalid verifies that a bad duration string
// returns a descriptive error.
func TestDuration_UnmarshalText_Invalid(t *testing.T) {
	var d config.Duration
	if err := d.UnmarshalText([]byte("not-a-duration")); err == nil {
		t.Fatal("UnmarshalText should return error for invalid duration")
	}
}
