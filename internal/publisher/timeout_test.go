package publisher

import (
	"errors"
	"strings"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// ── Fakes for paho's Client and Token interfaces ──────────────────────────
//
// These live in the internal test package so they can construct an
// MQTTPublisher directly; NewMQTTPublisher needs a real broker.

// fakeToken models a publish that either completes or never does.
type fakeToken struct {
	completes bool
	err       error
	done      chan struct{}
}

func newFakeToken(completes bool, err error) *fakeToken {
	ch := make(chan struct{})
	if completes {
		close(ch)
	}
	return &fakeToken{completes: completes, err: err, done: ch}
}

func (t *fakeToken) Wait() bool { <-t.done; return true }

// WaitTimeout reports false when the token does not complete in time, which is
// exactly the stall this timeout exists to bound.
func (t *fakeToken) WaitTimeout(d time.Duration) bool {
	if t.completes {
		return true
	}
	time.Sleep(d)
	return false
}

func (t *fakeToken) Done() <-chan struct{} { return t.done }
func (t *fakeToken) Error() error          { return t.err }

// fakeClient implements mqtt.Client with only the behaviour Publish needs.
type fakeClient struct {
	connected    bool
	token        mqtt.Token
	publishCalls int
}

func (c *fakeClient) IsConnected() bool      { return c.connected }
func (c *fakeClient) IsConnectionOpen() bool { return c.connected }
func (c *fakeClient) Connect() mqtt.Token    { return newFakeToken(true, nil) }
func (c *fakeClient) Disconnect(uint)        {}

func (c *fakeClient) Publish(string, byte, bool, interface{}) mqtt.Token {
	c.publishCalls++
	return c.token
}

func (c *fakeClient) Subscribe(string, byte, mqtt.MessageHandler) mqtt.Token {
	return newFakeToken(true, nil)
}

func (c *fakeClient) SubscribeMultiple(map[string]byte, mqtt.MessageHandler) mqtt.Token {
	return newFakeToken(true, nil)
}

func (c *fakeClient) Unsubscribe(...string) mqtt.Token     { return newFakeToken(true, nil) }
func (c *fakeClient) AddRoute(string, mqtt.MessageHandler) {}
func (c *fakeClient) OptionsReader() mqtt.ClientOptionsReader {
	return mqtt.ClientOptionsReader{}
}

func testMsg() Message {
	return Message{Topic: "ups/test/ups/status", Payload: "OL", Retained: true}
}

// ── Tests ─────────────────────────────────────────────────────────────────

func TestPublish_Success(t *testing.T) {
	c := &fakeClient{connected: true, token: newFakeToken(true, nil)}
	p := &MQTTPublisher{client: c, qos: 1, timeout: time.Second}

	if err := p.Publish(testMsg()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.publishCalls != 1 {
		t.Errorf("publishCalls = %d, want 1", c.publishCalls)
	}
}

// The regression this guards: token.Wait() had no deadline, so a publish that
// never completes blocked the poll loop for ever.
func TestPublish_TimesOutRatherThanBlocking(t *testing.T) {
	c := &fakeClient{connected: true, token: newFakeToken(false, nil)}
	p := &MQTTPublisher{client: c, qos: 1, timeout: 50 * time.Millisecond}

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- p.Publish(testMsg()) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected a timeout error")
		}
		if !strings.Contains(err.Error(), "timed out") {
			t.Errorf("error = %v, want a timeout", err)
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Errorf("took %s to give up, want ~50ms", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked past its timeout — the poll loop would hang here")
	}
}

// A disconnected client must fail instantly.  With ~50 messages per poll,
// waiting out the timeout on each would stall the loop for minutes.
func TestPublish_DisconnectedFailsImmediately(t *testing.T) {
	c := &fakeClient{connected: false, token: newFakeToken(false, nil)}
	p := &MQTTPublisher{client: c, qos: 1, timeout: 30 * time.Second}

	start := time.Now()
	err := p.Publish(testMsg())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error when disconnected")
	}
	if !strings.Contains(err.Error(), "not connected") {
		t.Errorf("error = %v, want a not-connected error", err)
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("took %s to fail, want immediate — the timeout must not be waited out", elapsed)
	}
	if c.publishCalls != 0 {
		t.Errorf("publishCalls = %d, want 0 — nothing should be sent while disconnected", c.publishCalls)
	}
}

// A broker-side error must still be surfaced.
func TestPublish_TokenErrorPropagated(t *testing.T) {
	want := errors.New("broker rejected the message")
	c := &fakeClient{connected: true, token: newFakeToken(true, want)}
	p := &MQTTPublisher{client: c, qos: 1, timeout: time.Second}

	if err := p.Publish(testMsg()); !errors.Is(err, want) {
		t.Errorf("error = %v, want %v", err, want)
	}
}

// A whole poll's worth of messages against a dead broker must fail fast rather
// than multiplying the timeout by the message count.
func TestPublish_FullPollAgainstDeadBrokerIsFast(t *testing.T) {
	c := &fakeClient{connected: false, token: newFakeToken(false, nil)}
	p := &MQTTPublisher{client: c, qos: 1, timeout: 5 * time.Second}

	start := time.Now()
	for i := 0; i < 52; i++ {
		if err := p.Publish(testMsg()); err == nil {
			t.Fatal("expected every publish to fail")
		}
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("52 publishes took %s; a dead broker must not stall the poll loop", elapsed)
	}
}

func TestDefaultPublishTimeout_ShorterThanTypicalPoll(t *testing.T) {
	if DefaultPublishTimeout >= 10*time.Second {
		t.Errorf("DefaultPublishTimeout = %s, must be shorter than the 10s poll interval",
			DefaultPublishTimeout)
	}
}
