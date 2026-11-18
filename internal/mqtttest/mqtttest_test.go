package mqtttest

import (
	"strings"
	"testing"
)

// Ubuntu 26.04's apparmor package confines /usr/sbin/mosquitto to
// /etc/mosquitto; the only sign of it in the broker's own output is that it
// cannot open a config file that plainly exists.  The failure must say why.
func TestStartFailureHint_AppArmor(t *testing.T) {
	logs := "1795082638: Error: Unable to open config file /tmp/x/mosquitto.conf.\n"
	hint := startFailureHint(logs)
	for _, want := range []string{"AppArmor", "/etc/apparmor.d/mosquitto", "apparmor_parser -R"} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint %q does not mention %q", hint, want)
		}
	}
}

func TestStartFailureHint_OtherFailures(t *testing.T) {
	for _, logs := range []string{
		"",
		"1795082638: Error: Address already in use\n",
	} {
		if hint := startFailureHint(logs); hint != "" {
			t.Errorf("startFailureHint(%q) = %q, want none", logs, hint)
		}
	}
}
