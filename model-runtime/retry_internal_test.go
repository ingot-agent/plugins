package modelruntime

import (
	"errors"
	"testing"
	"time"
)

type delayHint time.Duration

func (d delayHint) Error() string                     { return "transient" }
func (d delayHint) RetryDelay() (time.Duration, bool) { return time.Duration(d), true }

func TestRetryPolicyBounds(t *testing.T) {
	p, err := configuredRetry(Config{})
	if err != nil || p.maxAttempts != 8 {
		t.Fatalf("policy=%+v err=%v", p, err)
	}
	for attempt, capDelay := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second} {
		for i := 0; i < 100; i++ {
			delay, ok := p.delay(delayHint(0), attempt+1)
			if !ok || delay < capDelay/2 || delay > capDelay {
				t.Fatalf("attempt=%d delay=%v", attempt+1, delay)
			}
		}
	}
	if delay, ok := p.delay(delayHint(10*time.Second), 1); !ok || delay != 10*time.Second {
		t.Fatalf("provider hint=%v %v", delay, ok)
	}
	if delay, ok := p.delay(delayHint(time.Hour), 1); !ok || delay != 30*time.Second {
		t.Fatalf("provider cap=%v %v", delay, ok)
	}
	if _, ok := p.delay(errors.New("permanent"), 1); ok {
		t.Fatal("unmarked error retried")
	}
}

func TestRetryConfigurationValidation(t *testing.T) {
	for _, cfg := range []Config{{RetryMaxAttempts: -1}, {RetryMaxAttempts: 21}, {RetryInitialDelayMS: -1}, {RetryInitialDelayMS: 60001}, {RetryMaxDelayMS: -1}, {RetryMaxDelayMS: 300001}, {RetryInitialDelayMS: 2000, RetryMaxDelayMS: 1000}} {
		if err := validateConfig(cfg); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("config=%+v err=%v", cfg, err)
		}
	}
	p, err := configuredRetry(Config{RetryMaxAttempts: 1, RetryInitialDelayMS: 2, RetryMaxDelayMS: 8})
	if err != nil || p.maxAttempts != 1 || p.initialDelay != 2*time.Millisecond || p.maxDelay != 8*time.Millisecond {
		t.Fatalf("policy=%+v err=%v", p, err)
	}
}

func TestConfiguredMaximumHonorsLongProviderHints(t *testing.T) {
	p, err := configuredRetry(Config{RetryMaxDelayMS: 300000})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		hint time.Duration
		want time.Duration
	}{
		{2 * time.Minute, 2 * time.Minute},
		{time.Hour, 5 * time.Minute},
	} {
		if delay, ok := p.delay(delayHint(tc.hint), 1); !ok || delay != tc.want {
			t.Fatalf("hint=%v delay=%v retry=%v, want %v", tc.hint, delay, ok, tc.want)
		}
	}
}
