package main

import (
	"bytes"
	"encoding/json"
	"fmt"
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
	// OL at 0 and 2 s, OB from 4 s to 14 s, OL from 16 s.
	d, pub, clk := samplingDaemon(ol, ol, ob, ob, ob, ob, ob, ob, ol)
	t0 := time.Now()

	sampleAt(d, clk, t0, 3)
	if _, ok := pub.Retained(sampleOutageTopic); !ok {
		t.Fatal("outage not published at the sample that saw OB")
	}
	before := len(pub.Messages)

	clk.Advance(2 * time.Second)
	d.sample(t0.Add(6*time.Second), false) // OB again: nothing new
	if len(pub.Messages) != before {
		t.Errorf("published %d messages at an unchanged sample", len(pub.Messages)-before)
	}

	for _, at := range []time.Duration{8, 10, 12, 14, 16} {
		clk.Advance(2 * time.Second)
		d.sample(t0.Add(at*time.Second), false)
	}
	if m, ok := pub.Retained(sampleOutageTopic); ok {
		t.Errorf("outage still retained at the sample that saw mains return: %s", m.Payload)
	}
	if _, ok := pub.Retained(sampleLastTopic); !ok {
		t.Error("last_outage not published when mains returned")
	}
}

// The summary's duration comes from the samples: 4 s on battery is reported
// as 4 s, not rounded to the 10 s poll — even though mains returning so soon
// after the outage was published waits for the next scheduled publish.
func TestSample_OutageTimedToTheSample(t *testing.T) {
	ol, ob := reading("OL", "100"), reading("OB DISCHRG", "100")
	d, pub, clk := samplingDaemon(ol, ob, ob, ol) // OB from 2 s to 6 s
	sampleAt(d, clk, time.Now(), 4)
	if _, ok := pub.Retained(sampleLastTopic); ok {
		t.Error("published at 6 s, 4 s after the last change; a second change within a poll must wait")
	}
	t0 := time.Now()
	for _, at := range []time.Duration{8, 10, 12} {
		clk.Advance(2 * time.Second)
		d.sample(t0.Add(at*time.Second), false)
	}

	msg, ok := pub.Retained(sampleLastTopic)
	if !ok {
		t.Fatal("no last_outage at the scheduled publish")
	}
	var lo publisher.LastOutageMessage
	if err := json.Unmarshal([]byte(msg.Payload), &lo); err != nil {
		t.Fatal(err)
	}
	if lo.DurationSecs != 4 {
		t.Errorf("duration_secs = %v, want 4", lo.DurationSecs)
	}
}

// Power flapping every sample must not publish on every flip: changes go
// out at most once per poll interval, so the event rate the bridge queue is
// sized for holds.
func TestSample_FlappingPublishesAtMostOncePerPoll(t *testing.T) {
	ol, ob := reading("OL", "100"), reading("OB DISCHRG", "100")
	var seq [][]nut.Variable
	for i := 0; i < 31; i++ { // 60 s, flipping every 2 s
		if i%2 == 0 {
			seq = append(seq, ol)
		} else {
			seq = append(seq, ob)
		}
	}
	d, pub, clk := samplingDaemon(seq...)
	sampleAt(d, clk, time.Now(), len(seq))

	if got := count(pub, sampleStateTopic); got > 7 {
		t.Errorf("state published %d times in 60 s of flapping, want at most one per 10 s poll (7)", got)
	}
	if got := count(pub, sampleOutageTopic); got > 7 {
		t.Errorf("outage topic published %d times in 60 s of flapping, want at most 7", got)
	}
}

// A sample interval that does not divide the poll interval must not publish
// more often than once per poll: it drifts later instead.
func TestSample_NonDivisorNeverPublishesFaster(t *testing.T) {
	d, pub, _ := samplingDaemon(reading("OL", "100"))
	d.cfg.NUT.SampleInterval = config.Duration{Duration: 3 * time.Second}
	t0 := time.Now()
	var at []time.Time
	for i := 0; i <= 40; i++ { // 120 s
		n := count(pub, sampleStateTopic)
		now := t0.Add(time.Duration(i) * 3 * time.Second)
		d.sample(now, false)
		if count(pub, sampleStateTopic) > n {
			at = append(at, now)
		}
	}
	for i := 1; i < len(at); i++ {
		if gap := at[i].Sub(at[i-1]); gap < 10*time.Second-250*time.Millisecond {
			t.Errorf("published %s after the previous one, want at least the 10 s poll", gap)
		}
	}
}

// Low battery is news too, and its first onset is not held back even right
// after the outage was published.
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
		reading("OL CHRG", "80"), // mains: published at the next poll
	)
	sampleAt(d, clk, time.Now(), 7) // to 12 s

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
// on every 2 s sample — even when each error names a different local port,
// as a re-dial's does.
func TestSample_PollErrorLoggedOncePerPoll(t *testing.T) {
	var buf bytes.Buffer
	out, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(out); log.SetFlags(flags) })

	d, _, _ := samplingDaemon(reading("OL", "100"))
	fp := d.poller.(*nut.FakePoller)
	t0 := time.Now()
	for i := 0; i < 11; i++ { // 20 s of samples
		fp.Err = fmt.Errorf("read tcp 127.0.0.1:%d->127.0.0.1:3493: i/o timeout", 50000+i)
		d.sample(t0.Add(time.Duration(i)*2*time.Second), false)
	}
	if n := strings.Count(buf.String(), "i/o timeout"); n != 3 {
		t.Errorf("logged %d times over 20 s, want 3 (0, 10, 20 s):\n%s", n, buf.String())
	}
	if !strings.Contains(buf.String(), "11 failed samples so far") {
		t.Errorf("the repeat line should count the failures:\n%s", buf.String())
	}

	fp.Err = nil
	d.sample(t0.Add(22*time.Second), false)
	if !strings.Contains(buf.String(), "NUT answering again after 11 failed samples") {
		t.Errorf("recovery not logged:\n%s", buf.String())
	}
}

// Low battery flapping with mains — a flat battery and a flickering supply —
// publishes at most twice a poll: the scheduled publish and one exempt
// low-battery onset.
func TestSample_LowBatteryFlappingStaysWithinTheCap(t *testing.T) {
	ol, lb := reading("OL", "5"), reading("OB DISCHRG LB", "5")
	var seq [][]nut.Variable
	for i := 0; i < 31; i++ {
		if i%2 == 0 {
			seq = append(seq, ol)
		} else {
			seq = append(seq, lb)
		}
	}
	d, pub, clk := samplingDaemon(seq...)
	sampleAt(d, clk, time.Now(), len(seq))
	if got := count(pub, sampleStateTopic); got > 13 {
		t.Errorf("state published %d times in 60 s, want at most two a poll (13)", got)
	}
}

// A second outage gets its own first low battery published at once, even
// when it starts right after the first ended: the first outage's low battery
// does not count against it.
func TestSample_NewOutageLowBatteryNotHeldBack(t *testing.T) {
	ol, ob, lb := reading("OL", "50"), reading("OB DISCHRG", "50"), reading("OB DISCHRG LB", "10")
	d, pub, clk := samplingDaemon(lb, ol, ob, lb)
	t0 := time.Now()
	d.sample(t0, false) // outage 1, low battery: published
	for _, at := range []time.Duration{10, 12} {
		clk.Advance(10 * time.Second)
		d.sample(t0.Add(at*time.Second), false) // 10 s: mains, published; 12 s: outage 2, held back
	}
	before := count(pub, sampleOutageTopic)
	clk.Advance(2 * time.Second)
	d.sample(t0.Add(14*time.Second), false) // outage 2 reaches low battery
	if count(pub, sampleOutageTopic) == before {
		t.Error("the second outage's low battery was held back")
	}
}
