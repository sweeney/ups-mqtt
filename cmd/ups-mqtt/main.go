package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/sweeney/ups-mqtt/internal/config"
	"github.com/sweeney/ups-mqtt/internal/metrics"
	"github.com/sweeney/ups-mqtt/internal/nut"
	"github.com/sweeney/ups-mqtt/internal/outages"
	"github.com/sweeney/ups-mqtt/internal/publisher"
	"github.com/sweeney/ups-mqtt/internal/retry"
)

func main() {
	configPath := flag.String("config", "/etc/ups-mqtt/config.toml", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*configPath, "./config.toml")
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}

	log.Printf("ups-mqtt starting (NUT: %s:%d, UPS: %s, label: %s, MQTT: %s)",
		cfg.NUT.Host, cfg.NUT.Port, cfg.NUT.UPSName, cfg.NUT.EffectiveLabel(), cfg.MQTT.Broker)

	// The clock's monotonic zero is process start, which is also the best
	// estimate for the end of an outage that ended while nothing was running.
	tracker := outages.NewTracker(outages.NewSystemClock(), outageStore(cfg))

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	// Connect to MQTT broker first so LWT is registered before we talk to NUT.
	lwtTopic := publisher.StateTopic(cfg.MQTT.TopicPrefix, cfg.NUT.EffectiveLabel())
	lwtPayload := publisher.FormatOffline()

	pub, err := publisher.NewMQTTPublisher(cfg.MQTT, lwtTopic, lwtPayload)
	if err != nil {
		log.Fatalf("configuring MQTT: %v", err)
	}

	// The broker may not be reachable yet at boot: a local one still
	// starting, or a remote one whose name does not resolve until the network
	// is up.  Exiting and leaning on systemd's restart risks its start-rate
	// limit, after which nothing is published at all.
	connectMQTT := func() error { return pub.Connect(ctx) }
	if _, err := retry.Connect(ctx, "MQTT", startupBackoff, connectMQTT, retry.Sleep); err != nil {
		log.Printf("MQTT connection interrupted: %v", err)
		return
	}
	defer pub.Close() //nolint:errcheck
	log.Printf("connected to MQTT broker %s", cfg.MQTT.Broker)

	// Connect to NUT with exponential backoff, interruptible by signal.
	nutClient, err := connectNUT(ctx, cfg.NUT)
	if err != nil {
		log.Printf("NUT connection interrupted: %v", err)
		return
	}
	defer nutClient.Close() //nolint:errcheck
	log.Printf("connected to NUT at %s:%d", cfg.NUT.Host, cfg.NUT.Port)

	d := &daemon{
		cfg:         cfg,
		poller:      nutClient,
		pub:         pub,
		reconnected: pub.Reconnected(),
		outage:      outageState{tracker: tracker},
	}
	log.Printf("polling every %s, sampling every %s", cfg.NUT.PollInterval, cfg.NUT.EffectiveSampleInterval())
	d.run(ctx, lwtTopic)
}

// daemon is the poll loop and what it carries between samples.
type daemon struct {
	cfg    *config.Config
	poller nut.Poller
	pub    publisher.Publisher
	// reconnected signals that the broker connection was lost and is back.
	reconnected <-chan struct{}
	outage      outageState

	// What the last publish said, and when.  A sample that differs from it
	// in power state is published at once, at most once per poll.
	published         bool
	lastPublish       time.Time
	lastChangePublish time.Time
	lastOnBattery     bool
	lastLow           bool
	lowAnnounced      bool // low battery published in this outage

	// Failed samples in a row, and when one was last logged, so a NUT
	// outage is not logged on every sample.
	pollErrors    int
	lastPollErrAt time.Time
}

// run samples the UPS every sample interval until ctx is cancelled, then
// makes a final poll and announces the clean shutdown on lwtTopic.
//
// Everything is published every poll interval, and also at once when a
// sample finds the UPS has gone on or off battery or reached low battery:
// outage times are then accurate to a sample (2 s by default) rather than a
// poll, without publishing any more often.  The publish rate matters behind
// a bridge, whose queue holds the outage messages while its uplink is down.
//
// After a reconnect it publishes at once rather than waiting for the next
// poll, and re-sends the outage clear.  Publishes made while the connection
// was down failed and were not queued (see publisher.MQTTPublisher), so the
// broker's retained topics are brought back to the current truth: the
// outage payload or its clear, state, and any last_outage still due.
func (d *daemon) run(ctx context.Context, lwtTopic string) {
	ticker := time.NewTicker(d.cfg.NUT.EffectiveSampleInterval())
	defer ticker.Stop()

	d.sample(time.Now(), false)
loop:
	for {
		select {
		case now := <-ticker.C:
			d.sample(now, false)
		case <-d.reconnected:
			log.Printf("republishing current state after reconnecting")
			d.outage.clear = false
			d.sample(time.Now(), true)
		case <-ctx.Done():
			break loop
		}
	}

	log.Println("shutting down…")
	ticker.Stop()

	// Attempt a final poll so subscribers see fresh state on exit.  Against
	// a dead broker it fails fast rather than holding up the stop.
	if err := doPoll(d.poller, d.pub, d.cfg, &d.outage); err != nil {
		log.Printf("final poll failed (%v); skipping final state snapshot", err)
	}

	// Always publish the offline announcement.
	if err := publishOffline(d.pub, lwtTopic, d.cfg.MQTT.EffectiveEventQOS()); err != nil {
		log.Printf("publishing offline announcement: %v", err)
	}

	log.Println("offline announcement sent, exiting")
}

// sample reads the UPS once and publishes if a publish is due: force, a
// change of power state, or a poll interval since the last one.  Otherwise
// it only keeps the outage tracker current (its lowest battery charge).
func (d *daemon) sample(now time.Time, force bool) {
	vars, err := d.poller.Poll()
	if err != nil {
		d.logPollError(now, err)
		return
	}
	if d.pollErrors > 0 {
		log.Printf("NUT answering again after %d failed samples", d.pollErrors)
		d.pollErrors = 0
	}

	varMap := nut.VarsToMap(vars)
	m := metrics.Compute(varMap)
	d.track(varMap, m)

	poll := d.cfg.NUT.PollInterval.Duration
	// A little slack keeps ticker jitter from pushing a publish to the
	// sample after.  It is capped well below a sample, so where the sample
	// does not divide the poll interval, publishing drifts later, never
	// earlier: the publish rate is never more than once per poll.
	slack := min(d.cfg.NUT.EffectiveSampleInterval()/2, 250*time.Millisecond)
	due := now.Sub(d.lastPublish) >= poll-slack
	// A change of power state is published at once, but at most once per
	// poll interval: power flapping every couple of seconds must not
	// multiply the event rate the bridge queue is sized for.  A change it
	// holds back goes out with the next scheduled publish; the outage
	// times are still the samples', since the tracker follows every one.
	changed := m.OnBattery != d.lastOnBattery || m.LowBattery != d.lastLow
	changeAllowed := now.Sub(d.lastChangePublish) >= poll-slack
	// Low battery is the warning that the host is about to lose power, so
	// its first onset in an outage is not held back.  Like any change it
	// must differ from what was last published, so even low battery
	// flapping with mains publishes at most twice a poll: the scheduled
	// publish and one exempt change.
	firstLow := m.LowBattery && !d.lowAnnounced

	if !force && d.published && !due && !(changed && (changeAllowed || firstLow)) {
		return
	}
	if m.LowBattery {
		d.lowAnnounced = true
	}
	if changed {
		d.lastChangePublish = now
	}
	d.published, d.lastPublish = true, now
	d.lastOnBattery, d.lastLow = m.OnBattery, m.LowBattery
	if err := publishSample(varMap, m, d.pub, d.cfg, &d.outage); err != nil {
		log.Printf("poll error: %v", err)
	}
}

// track keeps the outage tracker current with every sample, published or
// not, so outage times are accurate to a sample.
func (d *daemon) track(varMap map[string]string, m metrics.Metrics) {
	tr := d.outage.tracker
	tr.Restore(m.OnBattery)
	if m.OnBattery {
		if _, isNew := tr.OnBattery(m.LowBattery, parseOptional(varMap["battery.charge"])); isNew {
			log.Printf("power outage detected — UPS on battery")
		}
		return
	}
	if tr.OnMains() {
		log.Printf("power restored")
		d.lowAnnounced = false // the next outage's low battery is news
	}
}

// logPollError logs a failed sample when NUT starts failing, then once a
// poll interval while it goes on.  It goes by time, not the error text: a
// re-dial's error names a new local port every time.
func (d *daemon) logPollError(now time.Time, err error) {
	d.pollErrors++
	if d.pollErrors > 1 && now.Sub(d.lastPollErrAt) < d.cfg.NUT.PollInterval.Duration {
		return
	}
	d.lastPollErrAt = now
	if d.pollErrors > 1 {
		log.Printf("poll error: polling NUT (%d failed samples so far): %v", d.pollErrors, err)
		return
	}
	log.Printf("poll error: polling NUT: %v", err)
}

// publishOffline announces a clean shutdown on the state topic, in the same
// shape the broker would publish as the LWT.  It is an event, so it goes at
// the event QoS like the LWT it stands in for.
func publishOffline(pub publisher.Publisher, topic string, qos byte) error {
	return pub.Publish(publisher.Message{
		Topic:    topic,
		Payload:  publisher.FormatOffline(),
		Retained: true,
		QOS:      qos,
	})
}

// connectNUT dials upsd, retrying until it succeeds or ctx is cancelled.
func connectNUT(ctx context.Context, cfg config.NUTConfig) (*nut.Client, error) {
	var c *nut.Client
	_, err := retry.Connect(ctx, "NUT", startupBackoff, func() error {
		var err error
		c, err = nut.NewClient(cfg.Host, cfg.Port, cfg.Username, cfg.Password, cfg.UPSName)
		return err
	}, retry.Sleep)
	return c, err
}

// startupBackoff paces the initial connects: 1 s, doubling, capped at 60 s.
var startupBackoff = retry.Backoff{Initial: time.Second, Max: 60 * time.Second}

// doPoll fetches NUT variables, computes metrics, and publishes everything.
//
// The outage message goes out FIRST, before the ~50 telemetry topics, and a
// telemetry failure never suppresses it.  Reporting a power cut is the job
// this daemon exists to do; queueing that message behind bulk telemetry on a
// link that is itself degraded by the outage is precisely backwards.
//
// Both halves are attempted on every poll and their errors returned together,
// so a failure in one is never hidden by the other.
func doPoll(poller nut.Poller, pub publisher.Publisher, cfg *config.Config, outage *outageState) error {
	vars, err := poller.Poll()
	if err != nil {
		return fmt.Errorf("polling NUT: %w", err)
	}
	varMap := nut.VarsToMap(vars)
	return publishSample(varMap, metrics.Compute(varMap), pub, cfg, outage)
}

// publishSample publishes one reading: the outage message first, then the
// telemetry.  See doPoll.
func publishSample(varMap map[string]string, m metrics.Metrics, pub publisher.Publisher, cfg *config.Config, outage *outageState) error {
	pubCfg := publisher.PublishConfig{
		Prefix:       cfg.MQTT.TopicPrefix,
		UPSName:      cfg.NUT.EffectiveLabel(),
		Retained:     cfg.MQTT.Retained,
		TelemetryQOS: cfg.MQTT.EffectiveTelemetryQOS(),
		EventQOS:     cfg.MQTT.EffectiveEventQOS(),
	}

	outageErr := publishOutageState(varMap, m, pubCfg, pub, outage)
	telemetryErr := publisher.PublishAll(varMap, m, pubCfg, pub)

	return errors.Join(outageErr, telemetryErr)
}

// outageStore returns where the outage in progress is persisted, so that a
// restart or power loss does not forget it.
func outageStore(cfg *config.Config) outages.Store {
	dir, source := cfg.EffectiveStateDir()
	if dir == "" {
		log.Printf("warning: no state directory (state_dir, or $STATE_DIRECTORY from the unit or plist); " +
			"an outage in progress will be forgotten across a restart")
		return outages.NopStore{}
	}
	path := filepath.Join(dir, outages.StateFileName)
	log.Printf("outage state: %s (from %s)", path, source)
	return &outages.FileStore{Path: path}
}

// outageState is what this process knows about the retained outage topic,
// plus the tracker following the outage itself.
type outageState struct {
	// clear is true once this process has confirmed the retained outage
	// topic empty.  It starts false: a previous process may have died
	// mid-outage — the host losing power is exactly how that happens — and
	// left its outage message retained on the broker, so the first poll on
	// mains clears it whether or not this process ever saw the outage.
	clear bool

	tracker *outages.Tracker
}

// publishOutageState publishes the outage message while the UPS is on battery,
// clears the retained topic once mains are restored, and then publishes the
// last_outage summary.
//
// The outage is marked clear only after the retained message has actually
// been cleared, so a failed clear is retried on the next poll rather than
// leaving a stale retained message asserting an outage that has already
// ended.  last_outage goes out only after the clear, and a failed publish is
// likewise retried on the next poll.
func publishOutageState(
	vars map[string]string,
	m metrics.Metrics,
	pubCfg publisher.PublishConfig,
	pub publisher.Publisher,
	st *outageState,
) error {
	tr := st.tracker
	tr.Restore(m.OnBattery)

	if m.OnBattery {
		st.clear = false
		o, isNew := tr.OnBattery(m.LowBattery, parseOptional(vars["battery.charge"]))
		if isNew {
			log.Printf("power outage detected — UPS on battery")
		}
		if err := publisher.PublishOutage(vars, m, o.StartedAt, o.ClockSynced, pubCfg, pub); err != nil {
			return fmt.Errorf("publishing outage: %w", err)
		}
		// A summary of an earlier outage may still be waiting.
		return publishLastOutage(tr, pubCfg, pub)
	}

	if tr.OnMains() {
		log.Printf("power restored")
	}
	if !st.clear {
		if err := publisher.ClearOutage(pubCfg, pub); err != nil {
			return fmt.Errorf("clearing outage: %w", err)
		}
		st.clear = true
	}
	return publishLastOutage(tr, pubCfg, pub)
}

// publishLastOutage publishes the summary of an ended outage, if one is due.
func publishLastOutage(tr *outages.Tracker, pubCfg publisher.PublishConfig, pub publisher.Publisher) error {
	s, ok := tr.Due()
	if !ok {
		return nil
	}
	if err := publisher.PublishLastOutage(s, pubCfg, pub); err != nil {
		return fmt.Errorf("publishing last_outage: %w", err)
	}
	tr.Published(s)

	kind := "last_outage"
	if !s.Final {
		kind = "interim last_outage (clock not synced; a corrected one will follow)"
	}
	log.Printf("published %s: %s → %s (%s, spans restart: %v)", kind,
		s.StartedAt.Format(time.RFC3339), s.EndedAt.Format(time.RFC3339), s.Duration(), s.SpansRestart)
	return nil
}

// parseOptional parses a numeric NUT value, or returns nil if it is absent
// or not a number.
func parseOptional(v string) *float64 {
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return nil
	}
	return &f
}
