package main

import (
	"context"
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

	log.Printf("ups-mqtt starting (NUT: %s:%d, UPS: %s, MQTT: %s)",
		cfg.NUT.Host, cfg.NUT.Port, cfg.NUT.UPSName, cfg.MQTT.Broker)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	// Connect to MQTT broker first so LWT is registered before we talk to NUT.
	lwtTopic := publisher.StateTopic(cfg.MQTT.TopicPrefix, cfg.NUT.UPSName)
	lwtPayload := publisher.FormatOffline()

	pub, err := publisher.NewMQTTPublisher(cfg.MQTT, lwtTopic, lwtPayload)
	if err != nil {
		log.Fatalf("connecting to MQTT broker: %v", err)
	}
	defer pub.Close() //nolint:errcheck

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

loop:
	for {
		select {
		case <-ticker.C:
			if err := doPoll(nutClient, pub, cfg); err != nil {
				log.Printf("poll error: %v", err)
			}
		case <-ctx.Done():
			break loop
		}
	}

	log.Println("shutting down…")
	ticker.Stop()

	// Attempt a final poll so subscribers see fresh state on exit.
	if err := doPoll(nutClient, pub, cfg); err != nil {
		log.Printf("final poll failed (%v); skipping final state snapshot", err)
	}

	// Always publish the offline announcement.
	offMsg := publisher.Message{
		Topic:    lwtTopic,
		Payload:  publisher.FormatOffline(),
		Retained: true,
	}
	if err := pub.Publish(offMsg); err != nil {
		log.Printf("publishing offline announcement: %v", err)
	}

	log.Println("offline announcement sent, exiting")
}

// connectNUT dials upsd with exponential backoff (1 s → 60 s cap).
// Each sleep is interruptible via ctx cancellation.
func connectNUT(ctx context.Context, cfg config.NUTConfig) (*nut.Client, error) {
	backoff := time.Second
	const maxBackoff = 60 * time.Second

	for {
		c, err := nut.NewClient(cfg.Host, cfg.Port, cfg.Username, cfg.Password, cfg.UPSName)
		if err == nil {
			return c, nil
		}
		log.Printf("NUT connection failed: %v — retrying in %s", err, backoff)

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// doPoll fetches NUT variables, computes metrics, and publishes everything.
func doPoll(poller nut.Poller, pub publisher.Publisher, cfg *config.Config) error {
	vars, err := poller.Poll()
	if err != nil {
		return fmt.Errorf("polling NUT: %w", err)
	}

	varMap := nut.VarsToMap(vars)
	m := metrics.Compute(varMap)

	pubCfg := publisher.PublishConfig{
		Prefix:   cfg.MQTT.TopicPrefix,
		UPSName:  cfg.NUT.UPSName,
		Retained: cfg.MQTT.Retained,
	}
	if err := publisher.PublishAll(varMap, m, pubCfg, pub); err != nil {
		return fmt.Errorf("publishing: %w", err)
	}
	return nil
}
