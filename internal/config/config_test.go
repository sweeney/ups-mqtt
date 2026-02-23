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

// TestLoad_ValidFile verifies that a well-formed TOML file is loaded and
// overrides the built-in defaults.
func TestLoad_ValidFile(t *testing.T) {
	f, err := os.CreateTemp("", "ups-mqtt-*.toml")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	defer os.Remove(f.Name())
	f.WriteString(`
[nut]
host          = "10.0.0.5"
port          = 4000
ups_name      = "myups"
poll_interval = "1m"

[mqtt]
broker       = "tcp://10.0.0.1:1883"
client_id    = "test-client"
topic_prefix = "home"
qos          = 0
retained     = false
`) //nolint:errcheck
	f.Close() //nolint:errcheck

	cfg, err := config.Load(f.Name())
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.NUT.Host != "10.0.0.5" {
		t.Errorf("NUT.Host = %q, want %q", cfg.NUT.Host, "10.0.0.5")
	}
	if cfg.NUT.Port != 4000 {
		t.Errorf("NUT.Port = %d, want 4000", cfg.NUT.Port)
	}
	if cfg.NUT.UPSName != "myups" {
		t.Errorf("NUT.UPSName = %q, want %q", cfg.NUT.UPSName, "myups")
	}
	if cfg.NUT.PollInterval.Duration != time.Minute {
		t.Errorf("NUT.PollInterval = %v, want 1m", cfg.NUT.PollInterval.Duration)
	}
	if cfg.MQTT.Broker != "tcp://10.0.0.1:1883" {
		t.Errorf("MQTT.Broker = %q, want tcp://10.0.0.1:1883", cfg.MQTT.Broker)
	}
	if cfg.MQTT.Retained {
		t.Error("MQTT.Retained should be false")
	}
	if cfg.MQTT.QOS != 0 {
		t.Errorf("MQTT.QOS = %d, want 0", cfg.MQTT.QOS)
	}
}

// TestLoad_EmptyPath verifies that an empty string in the paths list is
// silently skipped (hitting the continue branch).
func TestLoad_EmptyPath(t *testing.T) {
	cfg, err := config.Load("", "/nonexistent/path")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.NUT.Port != 3493 {
		t.Errorf("NUT.Port = %d, want default 3493", cfg.NUT.Port)
	}
}

// TestLoad_StatError verifies that a path that causes a non-IsNotExist stat
// error (e.g. a null byte in the path) is returned as an error.
func TestLoad_StatError(t *testing.T) {
	_, err := config.Load("path/with\x00null-byte")
	if err == nil {
		t.Fatal("Load() should return an error for a path with a null byte")
	}
}

// TestLoad_EnvOverride_NUTCredentials verifies Username, Password, and UPSName.
func TestLoad_EnvOverride_NUTCredentials(t *testing.T) {
	t.Setenv("UPS_MQTT_NUT_USERNAME", "admin")
	t.Setenv("UPS_MQTT_NUT_PASSWORD", "s3cr3t")
	t.Setenv("UPS_MQTT_NUT_UPS_NAME", "bigups")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.NUT.Username != "admin" {
		t.Errorf("NUT.Username = %q, want admin", cfg.NUT.Username)
	}
	if cfg.NUT.Password != "s3cr3t" {
		t.Errorf("NUT.Password = %q, want s3cr3t", cfg.NUT.Password)
	}
	if cfg.NUT.UPSName != "bigups" {
		t.Errorf("NUT.UPSName = %q, want bigups", cfg.NUT.UPSName)
	}
}

// TestLoad_EnvOverride_MQTTFields verifies Broker, Username, Password,
// ClientID, TopicPrefix, and TLSCACert.
func TestLoad_EnvOverride_MQTTFields(t *testing.T) {
	t.Setenv("UPS_MQTT_MQTT_BROKER", "ssl://mybroker:8883")
	t.Setenv("UPS_MQTT_MQTT_USERNAME", "mqttuser")
	t.Setenv("UPS_MQTT_MQTT_PASSWORD", "mqttpass")
	t.Setenv("UPS_MQTT_MQTT_CLIENT_ID", "my-client")
	t.Setenv("UPS_MQTT_MQTT_TOPIC_PREFIX", "home/ups")
	t.Setenv("UPS_MQTT_MQTT_TLS_CA_CERT", "/etc/ssl/ca.pem")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.MQTT.Broker != "ssl://mybroker:8883" {
		t.Errorf("MQTT.Broker = %q, want ssl://mybroker:8883", cfg.MQTT.Broker)
	}
	if cfg.MQTT.Username != "mqttuser" {
		t.Errorf("MQTT.Username = %q, want mqttuser", cfg.MQTT.Username)
	}
	if cfg.MQTT.Password != "mqttpass" {
		t.Errorf("MQTT.Password = %q, want mqttpass", cfg.MQTT.Password)
	}
	if cfg.MQTT.ClientID != "my-client" {
		t.Errorf("MQTT.ClientID = %q, want my-client", cfg.MQTT.ClientID)
	}
	if cfg.MQTT.TopicPrefix != "home/ups" {
		t.Errorf("MQTT.TopicPrefix = %q, want home/ups", cfg.MQTT.TopicPrefix)
	}
	if cfg.MQTT.TLSCACert != "/etc/ssl/ca.pem" {
		t.Errorf("MQTT.TLSCACert = %q, want /etc/ssl/ca.pem", cfg.MQTT.TLSCACert)
	}
}

// TestLoad_EnvOverride_Retained tests both truthy and falsy values.
func TestLoad_EnvOverride_Retained(t *testing.T) {
	for _, tc := range []struct {
		val  string
		want bool
	}{
		{"true", true},
		{"1", true},
		{"false", false},
		{"0", false},
	} {
		t.Run(tc.val, func(t *testing.T) {
			t.Setenv("UPS_MQTT_MQTT_RETAINED", tc.val)
			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("Load() error: %v", err)
			}
			if cfg.MQTT.Retained != tc.want {
				t.Errorf("MQTT.Retained = %v, want %v (env=%q)", cfg.MQTT.Retained, tc.want, tc.val)
			}
		})
	}
}

// TestLoad_EnvOverride_QOS_Valid verifies a valid QOS value is applied.
func TestLoad_EnvOverride_QOS_Valid(t *testing.T) {
	t.Setenv("UPS_MQTT_MQTT_QOS", "2")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.MQTT.QOS != 2 {
		t.Errorf("MQTT.QOS = %d, want 2", cfg.MQTT.QOS)
	}
}

// TestLoad_EnvOverride_QOS_Bad verifies that an invalid QOS value is silently
// ignored (with a log warning) and the default is kept.
func TestLoad_EnvOverride_QOS_Bad(t *testing.T) {
	t.Setenv("UPS_MQTT_MQTT_QOS", "not-a-number")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.MQTT.QOS != 1 {
		t.Errorf("MQTT.QOS = %d with bad env, want default 1", cfg.MQTT.QOS)
	}
}
