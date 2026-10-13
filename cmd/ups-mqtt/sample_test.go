package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/sweeney/ups-mqtt/internal/config"
	"github.com/sweeney/ups-mqtt/internal/nut"
	"github.com/sweeney/ups-mqtt/internal/outages"
	"github.com/sweeney/ups-mqtt/internal/publisher"
)

const (
	sampleStateTopic  = "ups/cyberpower/state"
	sampleOutageTopic = "ups/cyberpower/outage"
	sampleLastTopic   = "ups/cyberpower/last_outage"
)

func reading(status, charge string) []nut.Variable {
	return []nut.Variable{
		{Name: "ups.status", Value: status},
		{Name: "battery.charge", Value: charge},
		{Name: "input.voltage", Value: "240"},
	}
}

// samplingDaemon polls every 10 s and samples every 2 s, against a fake UPS
// that answers seq in turn and a fake broker.
func samplingDaemon(seq ...[]nut.Variable) (*daemon, *publisher.FakePublisher, *outages.FakeClock) {
	cfg := &config.Config{
		NUT: config.NUTConfig{
			UPSName:        "cyberpower",
			PollInterval:   config.Duration{Duration: 10 * time.Second},
			SampleInterval: config.Duration{Duration: 2 * time.Second},
		},
		MQTT: config.MQTTConfig{TopicPrefix: "ups", Retained: true},
	}
	st, clk, _ := newOutageStateWith(&outages.MemoryStore{})
	pub := &publisher.FakePublisher{}
	return &daemon{cfg: cfg, poller: &nut.FakePoller{Sequence: seq}, pub: pub, outage: *st}, pub, clk
}

// sampleAt runs one sample per reading, 2 s apart from t0, keeping the
// tracker's clock in step.
func sampleAt(d *daemon, clk *outages.FakeClock, t0 time.Time, n int) {
	for i := 0; i < n; i++ {
		if i > 0 {
			clk.Advance(2 * time.Second)
		}
		d.sample(t0.Add(time.Duration(i)*2*time.Second), false)
	}
}

func count(pub *publisher.FakePublisher, topic string) int {
	n := 0
	for _, m := range pub.Messages {
		if m.Topic == topic {
			n++
		}
	}
	return n
}

// Samples between polls read the UPS but publish nothing: the publish rate,
// which a bridge's queue depends on, is the poll interval's.
func TestSample_PublishesEveryPollNotEverySample(t *testing.T) {
	ol := reading("OL", "100")
	d, pub, clk := samplingDaemon(ol)
	sampleAt(d, clk, time.Now(), 11) // 0 s … 20 s

	if got := d.poller.(*nut.FakePoller).CallCount; got != 11 {
		t.Errorf("UPS read %d times, want every sample (11)", got)
	}
	if got := count(pub, sampleStateTopic); got != 3 {
		t.Errorf("state published %d times over 20 s, want 3 (0, 10, 20 s)", got)
	}
}

// Going on battery is published at the sample that sees it, and mains
// returning likewise, rather than at the next poll.
func TestSample_PowerChangePublishesAtOnce(t *testing.T) {
	ol, ob := reading("OL", "100"), reading("OB DISCHRG", "100")
	d, pub, clk := samplingDaemon(ol, ol, ob, ob, ol, ol)
	t0 := time.Now()

	sampleAt(d, clk, t0, 3) // 0 s OL (published), 2 s OL, 4 s OB
	if _, ok := pub.Retained(sampleOutageTopic); !ok {
		t.Fatal("outage not published at the sample that saw OB")
	}
	before := len(pub.Messages)

	clk.Advance(2 * time.Second)
	d.sample(t0.Add(6*time.Second), false) // OB again: nothing new
	if len(pub.Messages) != before {
		t.Errorf("published %d messages at an unchanged sample", len(pub.Messages)-before)
	}

	clk.Advance(2 * time.Second)
	d.sample(t0.Add(8*time.Second), false) // OL: mains back
	if m, ok := pub.Retained(sampleOutageTopic); ok {
		t.Errorf("outage still retained at the sample that saw mains return: %s", m.Payload)
	}
	if _, ok := pub.Retained(sampleLastTopic); !ok {
		t.Error("last_outage not published when mains returned")
	}
}

// The summary's duration comes from the samples: 4 s on battery is reported
// as 4 s, not rounded to the 10 s poll.
func TestSample_OutageTimedToTheSample(t *testing.T) {
	ol, ob := reading("OL", "100"), reading("OB DISCHRG", "100")
	d, pub, clk := samplingDaemon(ol, ob, ob, ol)
	sampleAt(d, clk, time.Now(), 4) // OB from 2 s to 6 s

	msg, ok := pub.Retained(sampleLastTopic)
	if !ok {
		t.Fatal("no last_outage")
	}
	var lo publisher.LastOutageMessage
	if err := json.Unmarshal([]byte(msg.Payload), &lo); err != nil {
		t.Fatal(err)
	}
	if lo.DurationSecs != 4 {
		t.Errorf("duration_secs = %v, want 4", lo.DurationSecs)
	}
}

// Low battery is news too.
func TestSample_LowBatteryPublishesAtOnce(t *testing.T) {
	ob, lb := reading("OB DISCHRG", "30"), reading("OB DISCHRG LB", "10")
	d, pub, clk := samplingDaemon(ob, ob, lb)
	sampleAt(d, clk, time.Now(), 3)

	msg, ok := pub.Retained(sampleOutageTopic)
	if !ok || !strings.Contains(msg.Payload, `"low_battery":true`) {
		t.Errorf("outage after the LB sample = %q, want low_battery true", msg.Payload)
	}
}

// Readings between publishes still count towards the lowest charge seen.
func TestSample_LowestChargeBetweenPublishes(t *testing.T) {
	d, pub, clk := samplingDaemon(
		reading("OL", "100"),
		reading("OB DISCHRG", "90"), // published: on battery
		reading("OB DISCHRG", "70"), // not published
		reading("OB DISCHRG", "80"),
		reading("OL CHRG", "80"),
	)
	sampleAt(d, clk, time.Now(), 5)

	msg, ok := pub.Retained(sampleLastTopic)
	if !ok {
		t.Fatal("no last_outage")
	}
	if !strings.Contains(msg.Payload, `"min_battery_charge_pct":70`) {
		t.Errorf("last_outage = %s, want the 70%% seen between publishes", msg.Payload)
	}
}

// A forced sample — after reconnecting — publishes whatever the schedule.
func TestSample_ForcePublishes(t *testing.T) {
	ol := reading("OL", "100")
	d, pub, _ := samplingDaemon(ol)
	t0 := time.Now()
	d.sample(t0, false)
	d.sample(t0.Add(time.Second), true)
	if got := count(pub, sampleStateTopic); got != 2 {
		t.Errorf("state published %d times, want 2", got)
	}
}

// A NUT outage is logged when it starts and then once a poll interval, not
// on every 2 s sample.
func TestSample_PollErrorLoggedOncePerPoll(t *testing.T) {
	var buf bytes.Buffer
	out, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(out); log.SetFlags(flags) })

	d, _, _ := samplingDaemon(reading("OL", "100"))
	d.poller.(*nut.FakePoller).Err = errors.New("connection refused")
	t0 := time.Now()
	for i := 0; i < 11; i++ { // 20 s of samples
		d.sample(t0.Add(time.Duration(i)*2*time.Second), false)
	}
	if n := strings.Count(buf.String(), "connection refused"); n != 3 {
		t.Errorf("logged %d times over 20 s, want 3 (0, 10, 20 s):\n%s", n, buf.String())
	}
}
