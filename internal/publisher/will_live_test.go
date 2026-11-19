package publisher

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sweeney/ups-mqtt/internal/config"
	"github.com/sweeney/ups-mqtt/internal/mqtttest"
	"github.com/sweeney/ups-mqtt/internal/retry"
)

// Against a real broker: the will that fires after a reconnect is the one
// built for that connection, not the one from the first.  This is the
// 2026-11-18 sequence: the first will is fired by the drop at the clock
// sync, the daemon reconnects, and a later death must not be announced with
// the first connection's unsynced clock_synced and timestamp.
func TestLiveWill_ReconnectRegistersAFreshWill(t *testing.T) {
	captureLog(t)
	broker := mqtttest.StartBroker(t, "")
	proxy := mqtttest.StartProxy(t, broker.Addr)
	rec := mqtttest.Record(t, broker.URL(), "ups/x/state")

	var n atomic.Int32
	will := func() string { return fmt.Sprintf(`{"online":false,"n":%d}`, n.Add(1)) }
	p, err := NewMQTTPublisherWith(config.MQTTConfig{Broker: proxy.URL(), ClientID: "will-test"}, "ups/x/state", will, Tuning{
		KeepAlive:      time.Second,
		PingTimeout:    time.Second,
		PublishTimeout: time.Second,
		Reconnect:      retry.Backoff{Initial: 100 * time.Millisecond, Max: 200 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := p.Connect(ctx); err != nil {
		t.Fatalf("Connect() = %v", err)
	}
	defer p.Close() //nolint:errcheck

	// fired waits for the broker to publish a will after a drop and returns
	// its n.
	fired := func(after int) int {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if got := rec.On("ups/x/state"); len(got) > after {
				var w struct{ N int }
				if err := json.Unmarshal([]byte(got[len(got)-1].Payload), &w); err != nil {
					t.Fatalf("will %q: %v", got[len(got)-1].Payload, err)
				}
				return w.N
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("no will fired:\n%s", broker.Logs())
		return 0
	}

	proxy.SetMode(mqtttest.Down) // the drop at the clock sync
	first := fired(0)
	proxy.SetMode(mqtttest.Up)
	select {
	case <-p.Reconnected():
	case <-time.After(10 * time.Second):
		t.Fatal("did not reconnect")
	}
	proxy.SetMode(mqtttest.Down) // a later death
	second := fired(1)

	if second <= first {
		t.Errorf("the will fired after the reconnect is n=%d, the first connection's was n=%d: "+
			"the reconnect re-registered the old will", second, first)
	}
}
