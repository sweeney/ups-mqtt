package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

// failingConnect fails n times, then succeeds, counting its calls.
func failingConnect(n int, calls *int) func() error {
	return func() error {
		*calls++
		if *calls <= n {
			return errors.New("dial tcp: lookup broker: no such host")
		}
		return nil
	}
}

// recordWaits is a wait func that records each backoff instead of sleeping.
func recordWaits(waits *[]time.Duration) func(context.Context, time.Duration) error {
	return func(ctx context.Context, d time.Duration) error {
		*waits = append(*waits, d)
		return ctx.Err()
	}
}

func TestRetryConnect_BacksOffToCapThenSucceeds(t *testing.T) {
	var calls int
	var waits []time.Duration
	err := retryConnect(context.Background(), "MQTT", failingConnect(8, &calls), recordWaits(&waits))
	if err != nil {
		t.Fatalf("retryConnect() = %v, want nil once the broker answers", err)
	}
	if calls != 9 {
		t.Errorf("connect called %d times, want 9 (8 failures + 1 success)", calls)
	}
	want := []time.Duration{1, 2, 4, 8, 16, 32, 60, 60}
	if len(waits) != len(want) {
		t.Fatalf("waits = %v, want %v seconds", waits, want)
	}
	for i, w := range want {
		if waits[i] != w*time.Second {
			t.Errorf("wait %d = %s, want %s", i, waits[i], w*time.Second)
		}
	}
}

func TestRetryConnect_FirstAttemptSucceeds_NoWait(t *testing.T) {
	var calls int
	var waits []time.Duration
	if err := retryConnect(context.Background(), "MQTT", failingConnect(0, &calls), recordWaits(&waits)); err != nil {
		t.Fatalf("retryConnect() = %v", err)
	}
	if calls != 1 || len(waits) != 0 {
		t.Errorf("calls = %d, waits = %v; want one call and no wait", calls, waits)
	}
}

// A SIGTERM while waiting between attempts must end the wait at once and
// return the context's error — a clean exit, not a fatal one, not a 60 s
// hang, and not one more attempt.
func TestRetryConnect_SignalDuringBackoffExitsCleanly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls int
	connect := func() error {
		calls++
		return errors.New("connection refused")
	}
	// The signal arrives once the backoff has begun.
	wait := func(ctx context.Context, d time.Duration) error {
		cancel()
		return sleepCtx(ctx, d)
	}

	start := time.Now()
	err := retryConnect(ctx, "MQTT", connect, wait)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("retryConnect() = %v, want context.Canceled", err)
	}
	if calls != 1 {
		t.Errorf("connect called %d times, want 1 — no attempt after the signal", calls)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("took %s to stop, want the 1 s backoff cut short", elapsed)
	}
}

// A SIGTERM during an attempt (Connect returns once ctx is cancelled) must
// exit without logging a failure or starting a backoff.
func TestRetryConnect_SignalDuringAttemptExitsCleanly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls, waits int
	connect := func() error {
		calls++
		cancel()
		return ctx.Err()
	}
	wait := func(context.Context, time.Duration) error {
		waits++
		return nil
	}

	err := retryConnect(ctx, "MQTT", connect, wait)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("retryConnect() = %v, want context.Canceled", err)
	}
	if calls != 1 || waits != 0 {
		t.Errorf("calls = %d, waits = %d; want 1 attempt and no backoff", calls, waits)
	}
}

func TestSleepCtx(t *testing.T) {
	if err := sleepCtx(context.Background(), time.Millisecond); err != nil {
		t.Errorf("sleepCtx() = %v, want nil after the wait", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := sleepCtx(ctx, time.Minute); !errors.Is(err, context.Canceled) {
		t.Errorf("sleepCtx() = %v, want context.Canceled", err)
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Error("a cancelled sleepCtx waited")
	}
}
