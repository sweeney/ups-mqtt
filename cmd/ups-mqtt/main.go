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
	log.Printf("polling every %s", cfg.NUT.PollInterval)
	d.run(ctx, cfg.NUT.PollInterval.Duration, lwtTopic)
}

// daemon is the poll loop and what it carries between polls.
type daemon struct {
	cfg    *config.Config
	poller nut.Poller
	pub    publisher.Publisher
	// reconnected signals that the broker connection was lost and is back.
	reconnected <-chan struct{}
	outage      outageState
}

// run polls every interval until ctx is cancelled, then makes a final poll
// and announces the clean shutdown on lwtTopic.
//
// After a reconnect it polls at once rather than waiting out the interval,
// and re-sends the outage clear.  Publishes made while the connection was
// down failed and were not queued (see publisher.MQTTPublisher), so the
// broker's retained topics are brought back to the current truth: the
// outage payload or its clear, state, and any last_outage still due.
func (d *daemon) run(ctx context.Context, interval time.Duration, lwtTopic string) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

loop:
	for {
		select {
		case <-ticker.C:
			d.poll()
		case <-d.reconnected:
			log.Printf("republishing current state after reconnecting")
			d.outage.clear = false
			d.poll()
			ticker.Reset(interval)
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

func (d *daemon) poll() {
	if err := doPoll(d.poller, d.pub, d.cfg, &d.outage); err != nil {
		log.Printf("poll error: %v", err)
	}
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
	m := metrics.Compute(varMap)

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
		log.Printf("warning: no state directory (StateDirectory= in the unit, or state_dir); " +
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
