package modelruntime

import (
	"context"
	"errors"
	"math/rand"
	"time"
)

const (
	maxProviderAttempts = 3
	maxRetryDelay       = 30 * time.Second
)

// retryHint is implemented by provider adapters, not by arbitrary interceptor
// errors. A true hint means another invocation may recover, not that the
// previous request was never processed or billed.
type retryHint interface {
	RetryDelay() (time.Duration, bool)
}

func retryDelay(err error, attempt int) (time.Duration, bool) {
	var hint retryHint
	if !errors.As(err, &hint) {
		return 0, false
	}
	serverDelay, ok := hint.RetryDelay()
	if !ok {
		return 0, false
	}
	// Full jitter with a bounded exponential cap, with the provider's
	// Retry-After treated as a minimum (also bounded).
	capDelay := time.Duration(1<<uint(attempt-1)) * 200 * time.Millisecond
	delay := time.Duration(rand.Int63n(int64(capDelay) + 1))
	if serverDelay > delay {
		delay = serverDelay
	}
	if delay > maxRetryDelay {
		delay = maxRetryDelay
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
