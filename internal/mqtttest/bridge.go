package mqtttest

import (
	"embed"
	"fmt"
	"strings"
	"testing"
)

//go:embed bridge/*.conf
var bridgeConf embed.FS

// BridgeNotificationTopic is where the bridge reports its link on the
// remote broker: retained "1" while connected, "0" (its will) once dropped.
const BridgeNotificationTopic = "sites/site/bridge"

// StartBridged runs the production local-broker and bridge config (the
// mirror in bridge/) bridged to uplink (host:port), and returns the local
// broker that ups-mqtt publishes to.
//
// The config runs as shipped except for what cannot be the same off the
// host, each of which must match or the start fails:
//   - the loopback listener moves to a free port;
//   - the password-protected network listener is dropped;
//   - the bridge's addresses become uplink;
//   - restart_timeout becomes 1 2, so a test does not wait the shipped 5 s
//     before the bridge reconnects.
func StartBridged(t testing.TB, uplink string) *Broker {
	t.Helper()
	port := FreePort(t)
	subs := []struct{ from, to string }{
		{"listener 1883 127.0.0.1", fmt.Sprintf("listener %d 127.0.0.1", port)},
		{"listener 1884\nallow_anonymous false\npassword_file /etc/mosquitto/passwd\n", ""},
		{"address broker.example-tailnet.ts.net:1883 100.64.0.20:1883", "address " + uplink},
		{"restart_timeout 5 60", "restart_timeout 1 2"},
	}
	var conf strings.Builder
	for _, name := range []string{"bridge/10-local.conf", "bridge/20-bridge.conf"} {
		b, err := bridgeConf.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		conf.Write(b)
		conf.WriteString("\n")
	}
	s := conf.String()
	for _, sub := range subs {
		if !strings.Contains(s, sub.from) {
			t.Fatalf("bridge config no longer contains %q; update StartBridged", sub.from)
		}
		s = strings.Replace(s, sub.from, sub.to, 1)
	}
	// Debian's main mosquitto.conf, which the shipped files rely on for
	// persistence, is not here; the tests do not restart the local broker.
	s += "persistence false\n"
	return StartBrokerConf(t, s, fmt.Sprintf("127.0.0.1:%d", port))
}
