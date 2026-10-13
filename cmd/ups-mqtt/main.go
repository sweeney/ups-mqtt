package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os/signal"
	"syscall"
	"time"

	"github.com/sweeney/ups-mqtt/internal/config"
	"github.com/sweeney/ups-mqtt/internal/metrics"
	"github.com/sweeney/ups-mqtt/internal/nut"
	"github.com/sweeney/ups-mqtt/internal/publisher"
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
	if err := retryConnect(ctx, "MQTT", connectMQTT, sleepCtx); err != nil {
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

	// Main poll loop.
	ticker := time.NewTicker(cfg.NUT.PollInterval.Duration)
	defer ticker.Stop()

	log.Printf("polling every %s", cfg.NUT.PollInterval)

	var outage outageState

loop:
	for {
		select {
		case <-ticker.C:
			if err := doPoll(nutClient, pub, cfg, &outage); err != nil {
				log.Printf("poll error: %v", err)
			}
		case <-ctx.Done():
			break loop
		}
	}

	log.Println("shutting down…")
	ticker.Stop()

	// Attempt a final poll so subscribers see fresh state on exit.
	if err := doPoll(nutClient, pub, cfg, &outage); err != nil {
		log.Printf("final poll failed (%v); skipping final state snapshot", err)
	}

	// Always publish the offline announcement.
	if err := publishOffline(pub, lwtTopic, cfg.MQTT.EffectiveEventQOS()); err != nil {
		log.Printf("publishing offline announcement: %v", err)
	}

	log.Println("offline announcement sent, exiting")
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
	err := retryConnect(ctx, "NUT", func() error {
		var err error
		c, err = nut.NewClient(cfg.Host, cfg.Port, cfg.Username, cfg.Password, cfg.UPSName)
		return err
	}, sleepCtx)
	return c, err
}

// Initial-connection backoff: 1 s, doubling, capped at 60 s.
const (
	initialBackoff = time.Second
	maxBackoff     = 60 * time.Second
)

// retryConnect calls connect until it succeeds, waiting between attempts with
// exponential backoff.  Each failure is logged once, with the wait that
// follows.  wait ends early when ctx is cancelled, and its error — ctx's — is
// returned, so a signal during backoff exits cleanly.
func retryConnect(
	ctx context.Context,
	what string,
	connect func() error,
	wait func(context.Context, time.Duration) error,
) error {
	backoff := initialBackoff
	for {
		err := connect()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err() // the attempt was cut short by a signal
		}
		log.Printf("%s connection failed: %v — retrying in %s", what, err, backoff)

		if err := wait(ctx, backoff); err != nil {
			return err
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// sleepCtx waits for d, or returns ctx's error if it is cancelled first.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

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

// outageState is what this process knows about the retained outage topic.
// Its zero value is the state at startup.
type outageState struct {
	// start is when the current on-battery condition began, or nil while on
	// mains.  It is used to compute the outage duration.
	start *time.Time

	// clear is true once this process has confirmed the retained outage
	// topic empty.  It starts false: a previous process may have died
	// mid-outage — the host losing power is exactly how that happens — and
	// left its outage message retained on the broker, so the first poll on
	// mains clears it whether or not this process ever saw the outage.
	clear bool
}

// publishOutageState publishes the outage message while the UPS is on battery,
// and clears the retained topic once mains are restored.
//
// The outage is marked clear only after the retained message has actually
// been cleared, so a failed clear is retried on the next poll rather than
// leaving a stale retained message asserting an outage that has already
// ended.
func publishOutageState(
	vars map[string]string,
	m metrics.Metrics,
	pubCfg publisher.PublishConfig,
	pub publisher.Publisher,
	outage *outageState,
) error {
	if m.OnBattery {
		outage.clear = false
		if outage.start == nil {
			now := time.Now()
			outage.start = &now
			log.Printf("power outage detected — UPS on battery")
		}
		if err := publisher.PublishOutage(vars, m, *outage.start, pubCfg, pub); err != nil {
			return fmt.Errorf("publishing outage: %w", err)
		}
		return nil
	}

	if !outage.clear {
		if err := publisher.ClearOutage(pubCfg, pub); err != nil {
			return fmt.Errorf("clearing outage: %w", err)
		}
		if outage.start != nil {
			log.Printf("power restored — outage topic cleared")
		}
		outage.start = nil
		outage.clear = true
	}
	return nil
}
