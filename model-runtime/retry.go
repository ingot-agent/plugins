package modelruntime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"time"

	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/observation"
)

const (
	defaultMaxAttempts    = 8
	defaultInitialDelayMS = 1000
	defaultMaxDelayMS     = 30000
)

type retryPolicy struct {
	maxAttempts  int
	initialDelay time.Duration
	maxDelay     time.Duration
}

func configuredRetry(cfg Config) (retryPolicy, error) {
	attempts, initial, maximum := cfg.RetryMaxAttempts, cfg.RetryInitialDelayMS, cfg.RetryMaxDelayMS
	if attempts == 0 {
		attempts = defaultMaxAttempts
	}
	if initial == 0 {
		initial = defaultInitialDelayMS
	}
	if maximum == 0 {
		maximum = defaultMaxDelayMS
	}
	if attempts < 1 || attempts > 20 || initial < 1 || initial > 60000 || maximum < initial || maximum > 300000 {
		return retryPolicy{}, fmt.Errorf("retry requires 1..20 attempts, 1..60000 ms initial delay and initial..300000 ms maximum delay: %w", ErrInvalidConfig)
	}
	return retryPolicy{attempts, time.Duration(initial) * time.Millisecond, time.Duration(maximum) * time.Millisecond}, nil
}

// retryHint is implemented by provider adapters, not by arbitrary interceptor
// errors. A true hint means another invocation may recover, not that the
// previous request was never processed or billed.
type retryHint interface {
	RetryDelay() (time.Duration, bool)
}

func (p retryPolicy) delay(err error, attempt int) (time.Duration, bool) {
	var hint retryHint
	if !errors.As(err, &hint) {
		return 0, false
	}
	serverDelay, ok := hint.RetryDelay()
	if !ok {
		return 0, false
	}
	// Equal jitter avoids immediate retry storms while allowing recovery over
	// seconds. Retry-After is a minimum, bounded by the configured maximum.
	capDelay := p.initialDelay
	for i := 1; i < attempt && capDelay < p.maxDelay; i++ {
		capDelay = min(capDelay*2, p.maxDelay)
	}
	delay := capDelay/2 + time.Duration(rand.Int63n(int64(capDelay-capDelay/2)+1))
	if serverDelay > delay {
		delay = serverDelay
	}
	if delay > p.maxDelay {
		delay = p.maxDelay
	}
	return delay, true
}

func waitRetry(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}

func logProviderRetry(ctx context.Context, request model.Request, attempt, limit int, delay time.Duration, stream bool) {
	attrs := []any{"provider", request.Provider, "model", request.Model,
		"attempt", attempt, "max_attempts", limit, "delay_ms", delay.Milliseconds(), "stream", stream}
	if correlation, ok := observation.CorrelationFromContext(ctx); ok {
		attrs = append(attrs, "session_id", correlation.SessionID, "turn_id", correlation.TurnID, "round", correlation.RoundIndex)
	}
	slog.WarnContext(ctx, "retrying model provider request", attrs...)
}
