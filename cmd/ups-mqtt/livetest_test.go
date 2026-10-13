package main

// Replays of the live power-cut tests in docs/live-tests.md against a real
// Mosquitto, with the link between ups-mqtt and the broker failed the way it
// failed live.  The UPS's status over time comes from each capture's
// ups.log; time is compressed so a run takes seconds.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sweeney/ups-mqtt/internal/config"
	"github.com/sweeney/ups-mqtt/internal/mqtttest"
	"github.com/sweeney/ups-mqtt/internal/nut"
	"github.com/sweeney/ups-mqtt/internal/outages"
	"github.com/sweeney/ups-mqtt/internal/publisher"
	"github.com/sweeney/ups-mqtt/internal/retry"
)

// compress is how much faster than real time the replays run: a 10 s poll
// takes 333 ms.  It is bounded below by MQTT's keep-alive, which has whole
// seconds as its unit; at 1 s it stands for a live 30 s, as configured.
const compress = 30

func scaled(d time.Duration) time.Duration { return d / compress }

// statusAt is the UPS's status from a moment in a capture onwards.
type statusAt struct {
	at     time.Duration // since the start of the timeline, live time
	status string
}

// timeline is what the UPS reported over a test, in live time.
type timeline []statusAt

// status returns what the UPS reported at live time at.
func (tl timeline) status(at time.Duration) string {
	s := tl[0].status
	for _, p := range tl {
		if p.at > at {
			break
		}
		s = p.status
	}
	return s
}

// onBattery returns when the UPS first went on battery and how long it
// stayed there, both in live time.
func (tl timeline) onBattery(t *testing.T) (start, dur time.Duration) {
	t.Helper()
	var end time.Duration
	found := false
	for i, p := range tl {
		ob := strings.Contains(p.status, "OB")
		if ob && !found {
			start, found = p.at, true
		}
		if found && !ob && end == 0 && i > 0 {
			end = p.at
		}
	}
	if !found || end == 0 {
		t.Fatal("timeline has no complete on-battery spell")
	}
	return start, end - start
}

// end is the live time of the last sample.
func (tl timeline) end() time.Duration { return tl[len(tl)-1].at }

var clockRE = regexp.MustCompile(`^(\d\d):(\d\d):(\d\d)(?:\.(\d{3}))?$`)

// parseClock turns HH:MM:SS[.mmm] into a duration since midnight.
func parseClock(t *testing.T, s string) time.Duration {
	t.Helper()
	m := clockRE.FindStringSubmatch(s)
	if m == nil {
		t.Fatalf("bad time %q", s)
	}
	var h, mi, sec, ms int
	fmt.Sscanf(m[1]+" "+m[2]+" "+m[3], "%d %d %d", &h, &mi, &sec) //nolint:errcheck
	if m[4] != "" {
		fmt.Sscanf(m[4], "%d", &ms) //nolint:errcheck
	}
	return time.Duration(h)*time.Hour + time.Duration(mi)*time.Minute +
		time.Duration(sec)*time.Second + time.Duration(ms)*time.Millisecond
}

// loadTimeline reads a capture's ups.log ("HH:MM:SS[.mmm] STATUS... VOLTS")
// from from to to (wall clock, HH:MM:SS), as offsets from from.
func loadTimeline(t *testing.T, capture, from, to string) timeline {
	t.Helper()
	lines, err := mqtttest.ReadLines(filepath.Join("..", "..", "testdata", "live-tests", "2026-10-13", capture, "ups.log"))
	if err != nil {
		t.Fatal(err)
	}
	start, stop := parseClock(t, from), parseClock(t, to)
	var tl timeline
	for _, l := range lines {
		f := strings.Fields(l)
		if len(f) < 3 {
			continue // upsc answered nothing that second
		}
		at := parseClock(t, f[0])
		if at < start || at > stop {
			continue
		}
		tl = append(tl, statusAt{at: at - start, status: strings.Join(f[1:len(f)-1], " ")})
	}
	if len(tl) == 0 {
		t.Fatalf("%s: no samples between %s and %s", capture, from, to)
	}
	return tl
}

// fault switches the link to mode at a live time in the timeline.
type fault struct {
	at   time.Duration
	mode mqtttest.Mode
}

// scriptedPoller answers as the UPS did at the current point of a timeline.
type scriptedPoller struct {
	tl    timeline
	start time.Time

	mu    sync.Mutex
	polls []time.Time
}

func (p *scriptedPoller) Poll() ([]nut.Variable, error) {
	now := time.Now()
	p.mu.Lock()
	p.polls = append(p.polls, now)
	p.mu.Unlock()
	status := p.tl.status(now.Sub(p.start) * compress)
	input := "240"
	if strings.Contains(status, "OB") {
		input = "0"
	}
	return []nut.Variable{
		{Name: "ups.status", Value: status},
		{Name: "battery.charge", Value: "100"},
		{Name: "input.voltage", Value: input},
	}, nil
}

func (p *scriptedPoller) Close() error { return nil }

// maxGap is the longest time between successive polls.
func (p *scriptedPoller) maxGap() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	var g time.Duration
	for i := 1; i < len(p.polls); i++ {
		g = max(g, p.polls[i].Sub(p.polls[i-1]))
	}
	return g
}

// realClock is the system clock with a known, synced wall clock, so a
// summary is published as soon as an outage ends.
type realClock struct{ start time.Time }

func (c realClock) Now() time.Time                  { return time.Now() }
func (c realClock) Mono() time.Duration             { return time.Since(c.start) }
func (c realClock) BootMono() (time.Duration, bool) { return 0, false }
func (c realClock) Synced() (bool, bool)            { return true, true }
func (c realClock) BootID() string                  { return "test-boot" }

// replayResult is what a replay left behind.
type replayResult struct {
	poller  *scriptedPoller
	rec     *mqtttest.Recorder
	observe string // URL of the broker subscribers read
	logs    string
}

// topology is where the daemon publishes, where subscribers read, and how
// the link between them is failed.
type topology struct {
	publishTo string
	observe   string
	setLink   func(mqtttest.Mode)
}

// direct is the topology of the live tests: ups-mqtt publishing straight
// to the broker over the failing link.
func direct(t *testing.T) topology {
	broker := mqtttest.StartBroker(t, "")
	proxy := mqtttest.StartProxy(t, broker.Addr)
	return topology{publishTo: proxy.URL(), observe: broker.URL(), setLink: proxy.SetMode}
}

// bridged is the topology since the cutover: ups-mqtt publishing to a local
// broker on its own host, bridged over the failing link to the main one.
func bridged(t *testing.T) topology {
	remote := mqtttest.StartBroker(t, "")
	proxy := mqtttest.StartProxy(t, remote.Addr)
	local := mqtttest.StartBridged(t, proxy.Addr)
	return topology{publishTo: local.URL(), observe: remote.URL(), setLink: proxy.SetMode}
}

const (
	replayLabel        = "site-ups"
	replayOutageTopic  = "ups/" + replayLabel + "/outage"
	replayStateTopic   = "ups/" + replayLabel + "/state"
	replayLastOutTopic = "ups/" + replayLabel + "/last_outage"
	replayTimeout      = 5 * time.Second / compress // the 5 s publish timeout, compressed
)

// replay runs the daemon through tl with the link failed as faults says,
// for settle (live time) after the timeline ends, publishing straight to the
// broker.  qos is the client's QoS for everything, as configured at the
// time of the test.
func replay(t *testing.T, tl timeline, faults []fault, settle time.Duration, qos byte) replayResult {
	t.Helper()
	return replayOn(t, direct, tl, faults, settle, func(c *config.MQTTConfig) { c.QOS = qos })
}

// replayOn is replay over a given topology, with the MQTT config adjusted
// by mqttCfg.
func replayOn(t *testing.T, topo func(*testing.T) topology, tl timeline, faults []fault, settle time.Duration,
	mqttCfg func(*config.MQTTConfig)) replayResult {
	t.Helper()
	if testing.Short() {
		t.Skip("live-test replay takes seconds")
	}

	var logs bytes.Buffer
	var logMu sync.Mutex
	out, flags := log.Writer(), log.Flags()
	log.SetOutput(writerFunc(func(b []byte) (int, error) {
		logMu.Lock()
		defer logMu.Unlock()
		return logs.Write(b)
	}))
	log.SetFlags(log.Lmicroseconds)
	t.Cleanup(func() { log.SetOutput(out); log.SetFlags(flags) })

	top := topo(t)
	rec := mqtttest.Record(t, top.observe, "#")

	cfg := &config.Config{
		NUT: config.NUTConfig{
			UPSName: "iplug", Label: replayLabel,
			PollInterval:   config.Duration{Duration: scaled(10 * time.Second)},
			SampleInterval: config.Duration{Duration: scaled(2 * time.Second)},
		},
		MQTT: config.MQTTConfig{
			Broker: top.publishTo, ClientID: "ups-mqtt-replay",
			TopicPrefix: "ups", Retained: true,
		},
	}
	mqttCfg(&cfg.MQTT)
	pub, err := publisher.NewMQTTPublisherWith(cfg.MQTT, replayStateTopic, publisher.FormatOffline(), publisher.Tuning{
		KeepAlive:      time.Second,
		PingTimeout:    time.Second,
		PublishTimeout: replayTimeout,
		Reconnect:      retry.Backoff{Initial: scaled(time.Second), Max: scaled(30 * time.Second)},
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := pub.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer pub.Close() //nolint:errcheck

	poller := &scriptedPoller{tl: tl, start: time.Now()}
	d := &daemon{
		cfg: cfg, poller: poller, pub: pub, reconnected: pub.Reconnected(),
		outage: outageState{tracker: outages.NewTracker(realClock{start: time.Now()}, &outages.MemoryStore{})},
	}
	for _, f := range faults {
		f := f
		time.AfterFunc(scaled(f.at), func() {
			log.Printf("replay: link %s", f.mode)
			top.setLink(f.mode)
		})
	}
	time.AfterFunc(scaled(tl.end()+settle), cancel)

	d.run(ctx, replayStateTopic)
	rec.WaitQuiet(200*time.Millisecond, 3*time.Second)

	logMu.Lock()
	res := replayResult{poller: poller, rec: rec, observe: top.observe, logs: logs.String()}
	logMu.Unlock()
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("daemon log:\n%s", res.logs)
		}
	})
	return res
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(b []byte) (int, error) { return f(b) }

var lastOutageLogRE = regexp.MustCompile(`published last_outage: \S+ → \S+ \(([^,]+), spans restart`)

// checkOutcome asserts what every replay must end with, whatever the link
// did: polls kept their pace, the outage is cleared on the broker, the
// summary's duration matches the UPS's, and nothing arrived out of order.
func checkOutcome(t *testing.T, r replayResult, tl timeline) {
	t.Helper()
	sample := scaled(2 * time.Second)

	// Samples keep their pace: at most one publish timeout per sample,
	// against the live 30 s polls when every publish waited one out.  The
	// slack of a poll interval absorbs a loaded CI runner; the live failure
	// was three poll intervals.
	if g := r.poller.maxGap(); g > sample+replayTimeout+scaled(10*time.Second) {
		t.Errorf("longest gap between samples %s, want at most ~%s (one sample plus one publish timeout)",
			g, sample+replayTimeout)
	}

	// The broker ends with the outage cleared and the summary retained.
	if p, ok := mqtttest.Retained(t, r.observe, replayOutageTopic); ok {
		t.Errorf("broker still retains an outage after mains returned: %s", p)
	}
	if _, ok := mqtttest.Retained(t, r.observe, replayLastOutTopic); !ok {
		t.Error("no last_outage retained")
	}

	// The summary's duration is the UPS's, to within a sample at each end
	// (the capture itself was sampled every 1–2 s).
	m := lastOutageLogRE.FindStringSubmatch(r.logs)
	if m == nil {
		t.Fatal("no last_outage published")
	}
	got, err := time.ParseDuration(m[1])
	if err != nil {
		t.Fatal(err)
	}
	_, ups := tl.onBattery(t)
	if diff := time.Duration(math.Abs(float64(got - scaled(ups)))); diff > 2*sample+scaled(2*time.Second) {
		t.Errorf("last_outage lasted %s (live %s), UPS was on battery %s live; off by more than a sample at each end",
			got, got*compress, ups)
	}

	// Nothing arrives out of order: every state is newer than the one
	// before it, and nothing follows the final clear of the outage topic.
	var prev time.Time
	for _, m := range r.rec.On(replayStateTopic) {
		var s struct {
			Timestamp time.Time `json:"timestamp"`
			Online    *bool     `json:"online"`
		}
		if json.Unmarshal([]byte(m.Payload), &s) != nil || s.Timestamp.IsZero() {
			t.Errorf("unparseable state %q", m.Payload)
			continue
		}
		if s.Online != nil && !*s.Online {
			// The LWT or the offline announcement.  The LWT is stamped
			// when the client connected, so it is "old" by design.
			continue
		}
		if s.Timestamp.Before(prev) {
			t.Errorf("state %s arrived after %s: delivered out of order", s.Timestamp.Format(time.RFC3339Nano), prev.Format(time.RFC3339Nano))
		}
		prev = s.Timestamp
	}
	outs := r.rec.On(replayOutageTopic)
	if len(outs) == 0 || outs[len(outs)-1].Payload != "" {
		t.Errorf("last message on %s is %+v, want the clear", replayOutageTopic, outs)
	}
}

// Test 3: everything working.  The outage reaches the broker live, every
// poll, and the broker ends clean.
func TestLiveReplay_Clean(t *testing.T) {
	tl := loadTimeline(t, "3-clean", "20:04:30", "20:10:45")
	r := replay(t, tl, nil, 15*time.Second, 1)
	checkOutcome(t, r, tl)

	start, dur := tl.onBattery(t)

	// The outage reaches the broker within a sample of the UPS going on
	// battery, not at the next poll.
	var first time.Time
	for _, m := range r.rec.On(replayOutageTopic) {
		if m.Payload != "" {
			first = m.At
			break
		}
	}
	if first.IsZero() {
		t.Fatal("no outage message reached the broker")
	}
	if lag := first.Sub(r.poller.start.Add(scaled(start))); lag > scaled(6*time.Second) {
		t.Errorf("outage reached the broker %s (live %s) after the UPS went on battery, want within a sample",
			lag, lag*compress)
	}

	payloads := 0
	for _, m := range r.rec.On(replayOutageTopic) {
		if m.Payload != "" {
			payloads++
		}
	}
	if want := int(dur/(10*time.Second)) - 2; payloads < want {
		t.Errorf("%d outage messages for %s on battery, want about one per poll (≥%d)", payloads, dur, want)
	}
	if strings.Contains(r.logs, "connection to") {
		t.Errorf("connection trouble logged on a clean link:\n%s", r.logs)
	}
}

// Test 1: the network gear lost power with mains.  The link went half-open
// two seconds before the UPS reported OB and stayed dead until the router
// had rebooted, nearly two minutes after mains returned.  Its ups.log was
// lost; the timeline is the one recorded in docs/live-tests.md.
func TestLiveReplay_NetworkUnpowered(t *testing.T) {
	tl := timeline{
		{0, "OL"},                // 12:57:21
		{15 * time.Second, "OB"}, // 12:57:36
		{33 * time.Second, "OL"}, // 12:57:54
		{2*time.Minute + 50*time.Second, "OL"},
	}
	faults := []fault{
		{12 * time.Second, mqtttest.Blackhole},        // 12:57:33 eth0 down
		{2*time.Minute + 24*time.Second, mqtttest.Up}, // 12:59:45 back
	}
	r := replay(t, tl, faults, 15*time.Second, 1)
	checkOutcome(t, r, tl)
	checkReconnectLogged(t, r)
}

// Test 2: the broker's name stopped resolving hours before the cut, and only
// came back seven minutes after it.  The connection was half-open when it
// broke and every reconnect failed.
func TestLiveReplay_DNSBroken(t *testing.T) {
	tl := loadTimeline(t, "2-dns-broken", "19:46:30", "19:51:30")
	faults := []fault{
		{5 * time.Second, mqtttest.Blackhole},
		{tl.end() + 2*time.Minute, mqtttest.Up},
	}
	r := replay(t, tl, faults, 3*time.Minute, 1)
	checkOutcome(t, r, tl)
	checkReconnectLogged(t, r)

	// Nothing from the dead spell is replayed: once reconnected, the
	// broker hears current state, not a burst of what was missed.
	var during int
	for _, m := range r.rec.On(replayOutageTopic) {
		if m.Payload != "" {
			during++
		}
	}
	if during > 1 {
		t.Errorf("%d outage payloads reached the broker although the link was dead for the whole outage; "+
			"they were queued and replayed", during)
	}
}

func checkReconnectLogged(t *testing.T, r replayResult) {
	t.Helper()
	for _, want := range []string{"MQTT connection to", "lost:", "reconnected to MQTT broker", "republishing current state"} {
		if !strings.Contains(r.logs, want) {
			t.Errorf("daemon log lacks %q", want)
		}
	}
}

// Stopping against a dead broker is bounded: the final poll costs at most one
// publish timeout, and the offline announcement fails fast after it.  On a
// battery shutdown the UPS cuts power a fixed time after the host halts, so
// ups-mqtt must stop well inside the unit's TimeoutStopSec.
func TestShutdown_BoundedAgainstDeadBroker(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a broker")
	}
	captureLogTo(t)
	broker := mqtttest.StartBroker(t, "")
	proxy := mqtttest.StartProxy(t, broker.Addr)
	cfg := &config.Config{
		NUT: config.NUTConfig{
			UPSName: "iplug", Label: replayLabel,
			PollInterval: config.Duration{Duration: 10 * time.Second},
		},
		MQTT: config.MQTTConfig{
			Broker: proxy.URL(), ClientID: "ups-mqtt-stop",
			TopicPrefix: "ups", Retained: true, QOS: 1,
		},
	}
	const publishTimeout = time.Second
	pub, err := publisher.NewMQTTPublisherWith(cfg.MQTT, replayStateTopic, publisher.FormatOffline(), publisher.Tuning{
		KeepAlive: 30 * time.Second, PingTimeout: 10 * time.Second,
		PublishTimeout: publishTimeout,
		Reconnect:      retry.Backoff{Initial: time.Second, Max: 30 * time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := pub.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	poller := &nut.FakePoller{Variables: []nut.Variable{{Name: "ups.status", Value: "OB DISCHRG"}}}
	st, _, _ := newOutageStateWith(&outages.MemoryStore{})
	d := &daemon{cfg: cfg, poller: poller, pub: pub, reconnected: pub.Reconnected(), outage: *st}

	// The link dies half-open, as when the network loses power with mains;
	// the keep-alive has not noticed yet when the stop comes.
	proxy.SetMode(mqtttest.Blackhole)
	cancel()
	start := time.Now()
	d.run(ctx, replayStateTopic)
	pub.Close() //nolint:errcheck
	if took := time.Since(start); took > 2*publishTimeout+time.Second {
		t.Errorf("stopping took %s against a dead broker, want about one publish timeout (%s)", took, publishTimeout)
	}
}

// captureLogTo silences the standard logger for the rest of the test.
func captureLogTo(t *testing.T) {
	t.Helper()
	out := log.Writer()
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(out) })
}
