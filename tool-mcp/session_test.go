package toolmcp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	ingotabi "github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/httpx"
	"github.com/ingot-agent/sdk/logging"
)

func testPlatform(t *testing.T, config Config) *platform {
	t.Helper()
	normalized, err := normalizeConfig(config)
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	return newPlatform(t.TempDir(), ingotabi.None[httpx.Client](), logging.Discard(), nil, normalized)
}

func TestAcquireRejectsUnknownServer(t *testing.T) {
	instance := testPlatform(t, Config{})
	if _, err := instance.acquire(context.Background(), "missing"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestAcquireRejectsDisabledServer(t *testing.T) {
	instance := testPlatform(t, Config{Servers: []ServerConfig{{
		Name: "demo", Command: "true", Enabled: boolPointer(false),
	}}})
	if _, err := instance.acquire(context.Background(), "demo"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestBackoffFailsFastWithoutConnecting(t *testing.T) {
	instance := testPlatform(t, Config{Servers: []ServerConfig{{
		Name: "demo", Command: "definitely-not-a-real-executable",
	}}})
	runtime := instance.runtimeFor("demo")
	runtime.recordFailure(errors.New("connection refused"))

	start := time.Now()
	_, err := instance.acquire(context.Background(), "demo")
	if err == nil {
		t.Fatal("expected an error while backing off")
	}
	if !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("acquire blocked for %s instead of failing fast", elapsed)
	}

	status := runtime.status(serverStatus{Name: "demo"})
	if status.Status != "backoff" {
		t.Fatalf("status = %q", status.Status)
	}
	if status.ConsecutiveErrors != 1 {
		t.Fatalf("consecutive errors = %d", status.ConsecutiveErrors)
	}
	if status.LastError != "connection refused" {
		t.Fatalf("last error = %q", status.LastError)
	}
	if status.RetryAfterSeconds <= 0 {
		t.Fatalf("retry after = %v", status.RetryAfterSeconds)
	}
}

func TestBackoffGrowsAndIsCapped(t *testing.T) {
	runtime := &serverRuntime{name: "demo"}
	for attempt := 0; attempt < 20; attempt++ {
		runtime.recordFailure(errors.New("boom"))
	}
	runtime.mu.Lock()
	delay := time.Until(runtime.retryAt)
	runtime.mu.Unlock()
	if delay > backoffMaximum {
		t.Fatalf("backoff = %s, want at most %s", delay, backoffMaximum)
	}
}

func TestMarkStaleAndCloseAreIdempotent(t *testing.T) {
	instance := testPlatform(t, Config{Servers: []ServerConfig{{Name: "demo", Command: "true"}}})
	instance.markStale()
	instance.closeAll()
	instance.closeAll()
}

func TestAcquireSlotHonoursConcurrencyLimit(t *testing.T) {
	runtime := &serverRuntime{name: "demo"}
	release, err := runtime.acquireSlot(context.Background(), 1)
	if err != nil {
		t.Fatalf("acquireSlot: %v", err)
	}

	acquired := make(chan struct{})
	go func() {
		second, err := runtime.acquireSlot(context.Background(), 1)
		if err == nil {
			second()
			close(acquired)
		}
	}()

	select {
	case <-acquired:
		t.Fatal("two calls ran concurrently on a single-slot server")
	case <-time.After(50 * time.Millisecond):
	}

	release()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("the waiting call did not proceed after release")
	}
}

func TestAcquireSlotHonoursCancellation(t *testing.T) {
	runtime := &serverRuntime{name: "demo"}
	release, err := runtime.acquireSlot(context.Background(), 1)
	if err != nil {
		t.Fatalf("acquireSlot: %v", err)
	}
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runtime.acquireSlot(ctx, 1); err == nil {
		t.Fatal("expected a cancellation error")
	}
}
