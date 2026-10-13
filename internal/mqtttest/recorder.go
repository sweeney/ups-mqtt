package mqtttest

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

var clientSeq atomic.Int64

// Recorder subscribes to a topic filter and keeps every message, in arrival
// order.  It connects to the broker directly, not through a Proxy, so it
// stands for a subscriber on the far side of the failing link.
type Recorder struct {
	client mqtt.Client

	mu  sync.Mutex
	got []Received
}

// Record subscribes to filter on brokerURL at QoS 1 and records until the
// test ends.
func Record(t testing.TB, brokerURL, filter string) *Recorder {
	t.Helper()
	r := &Recorder{}
	opts := mqtt.NewClientOptions().
		AddBroker(brokerURL).
		SetClientID(fmt.Sprintf("recorder-%d", clientSeq.Add(1))).
		SetOrderMatters(true)
	r.client = mqtt.NewClient(opts)
	if tok := r.client.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("recorder connecting to %s: %v", brokerURL, tok.Error())
	}
	tok := r.client.Subscribe(filter, 1, func(_ mqtt.Client, m mqtt.Message) {
		r.mu.Lock()
		r.got = append(r.got, Received{At: time.Now(), Topic: m.Topic(), Payload: string(m.Payload()), Retained: m.Retained()})
		r.mu.Unlock()
	})
	if !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("recorder subscribing to %s: %v", filter, tok.Error())
	}
	t.Cleanup(func() { r.client.Disconnect(100) })
	return r
}

// Messages returns everything received so far.
func (r *Recorder) Messages() []Received {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Received(nil), r.got...)
}

// On returns the messages received on topic, in order.
func (r *Recorder) On(topic string) []Received {
	var out []Received
	for _, m := range r.Messages() {
		if m.Topic == topic {
			out = append(out, m)
		}
	}
	return out
}

// Retained returns the message the broker currently retains on topic, as a
// new subscriber would see it, or false when there is none.
func Retained(t testing.TB, brokerURL, topic string) (string, bool) {
	t.Helper()
	got := make(chan string, 1)
	opts := mqtt.NewClientOptions().
		AddBroker(brokerURL).
		SetClientID(fmt.Sprintf("retained-%d", clientSeq.Add(1)))
	c := mqtt.NewClient(opts)
	if tok := c.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("connecting to %s: %v", brokerURL, tok.Error())
	}
	defer c.Disconnect(100)
	tok := c.Subscribe(topic, 1, func(_ mqtt.Client, m mqtt.Message) {
		if m.Retained() {
			select {
			case got <- string(m.Payload()):
			default:
			}
		}
	})
	if !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("subscribing to %s: %v", topic, tok.Error())
	}
	select {
	case p := <-got:
		return p, true
	case <-time.After(300 * time.Millisecond):
		return "", false
	}
}
