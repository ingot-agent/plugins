package toolmcp

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ingot-agent/sdk/workspace"
	"strings"
	"testing"
	"time"

	ingotabi "github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/httpx"
	"github.com/ingot-agent/sdk/logging"
	"github.com/ingot-agent/sdk/tool"
)

func testPlatform(t *testing.T, config Config) *platform {
	t.Helper()
	normalized, err := normalizeConfig(config)
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	return newPlatform(t.TempDir(), ingotabi.None[httpx.Client](), ingotabi.None[workspace.Resolver](), logging.Discard(), nil, normalized)
}

func TestAcquireRejectsUnknownServer(t *testing.T) {
	instance := testPlatform(t, Config{})
	if _, err := instance.acquire(context.Background(), "missing", testScopeExecution("session-a")); err == nil {
		t.Fatal("expected an error")
	}
}

func TestAcquireRejectsDisabledServer(t *testing.T) {
	instance := testPlatform(t, Config{Servers: []ServerConfig{{
		Name: "demo", Command: "true", Enabled: boolPointer(false),
	}}})
	if _, err := instance.acquire(context.Background(), "demo", testScopeExecution("session-a")); err == nil {
		t.Fatal("expected an error")
	}
}

func TestBackoffFailsFastWithoutConnecting(t *testing.T) {
	instance := testPlatform(t, Config{Servers: []ServerConfig{{
		Name: "demo", Command: "definitely-not-a-real-executable",
	}}})
	runtime := mustRuntime(t, instance, connectionKey{server: "demo", session: "session-a"})
	runtime.recordFailure(errors.New("connection refused"))

	start := time.Now()
	_, err := instance.acquire(context.Background(), "demo", testScopeExecution("session-a"))
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
	slots := &serverSlots{}
	release, err := slots.acquire(context.Background(), 1)
	if err != nil {
		t.Fatalf("acquireSlot: %v", err)
	}

	acquired := make(chan struct{})
	go func() {
		second, err := slots.acquire(context.Background(), 1)
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
	slots := &serverSlots{}
	release, err := slots.acquire(context.Background(), 1)
	if err != nil {
		t.Fatalf("acquireSlot: %v", err)
	}
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := slots.acquire(ctx, 1); err == nil {
		t.Fatal("expected a cancellation error")
	}
}

func TestIdleTimeoutEvictsTheSession(t *testing.T) {
	dir := t.TempDir()
	if err := saveConfig(dir, helperConfig(t)); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	exports, cleanup := gateway(t, dir)
	runDiscovery(t, exports)
	cleanup()

	config := helperConfig(t)
	config.Servers[0].IdleTimeoutSeconds = 1
	config, err := normalizeConfig(config)
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	instance := newPlatform(dir, ingotabiNone(), ingotabi.None[workspace.Resolver](), discardLogger(), nil, config)
	defer instance.closeAll()

	first, err := instance.acquire(context.Background(), "helper", testScopeExecution("session-a"))
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	first.release()
	time.Sleep(1100 * time.Millisecond)

	second, err := instance.acquire(context.Background(), "helper", testScopeExecution("session-a"))
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	defer second.release()
	if first == second {
		t.Fatal("an idle session should have been replaced")
	}
}

func TestStatusReportsBackoffWithoutNetwork(t *testing.T) {
	config, err := normalizeConfig(Config{Servers: []ServerConfig{{
		Name: "demo", Command: "true",
	}}})
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	instance := newPlatform(t.TempDir(), ingotabiNone(), ingotabi.None[workspace.Resolver](), discardLogger(), nil, config)
	mustRuntime(t, instance, connectionKey{server: "demo", session: "s"}).recordFailure(errors.New("boom"))

	statuses := instance.statuses()
	if len(statuses) != 1 {
		t.Fatalf("statuses = %#v", statuses)
	}
	if statuses[0].Status != "backoff" {
		t.Fatalf("status = %q", statuses[0].Status)
	}
	if statuses[0].Connected {
		t.Fatal("a failed server must not report as connected")
	}
}

func TestStatusesReportDisabledServers(t *testing.T) {
	config, err := normalizeConfig(Config{Servers: []ServerConfig{{
		Name: "demo", Command: "true", Enabled: boolPointer(false),
	}}})
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	instance := newPlatform(t.TempDir(), ingotabiNone(), ingotabi.None[workspace.Resolver](), discardLogger(), nil, config)
	statuses := instance.statuses()
	if len(statuses) != 1 || statuses[0].Status != "disabled" {
		t.Fatalf("statuses = %#v", statuses)
	}
}

func TestMarkStaleForcesReconnectOnNextUse(t *testing.T) {
	dir := t.TempDir()
	if err := saveConfig(dir, helperConfig(t)); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	exports, cleanup := gateway(t, dir)
	runDiscovery(t, exports)
	cleanup()

	instance := newPlatform(dir, ingotabiNone(), ingotabi.None[workspace.Resolver](), discardLogger(), nil, helperConfig(t))
	defer instance.closeAll()

	first, err := instance.acquire(context.Background(), "helper", testScopeExecution("session-a"))
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	instance.markStale()
	second, err := instance.acquire(context.Background(), "helper", testScopeExecution("session-a"))
	if err != nil {
		first.release()
		t.Fatalf("second acquire: %v", err)
	}
	if first == second {
		t.Fatal("a stale session must be replaced")
	}
	// The retired session stays open until its last lease is released, then its
	// transport stops serving calls.
	first.release()
	if _, err := first.conn.session.ListTools(context.Background(), nil); err == nil {
		t.Fatal("the retired session should be closed after its last release")
	}
	second.release()
}

// TestReconfigurationDoesNotAbortInFlightCalls is the reason sessions are
// leased: retiring a session during a configuration change must not fail a call
// that is already running, because the model cannot tell whether its side
// effects happened.
func TestReconfigurationDoesNotAbortInFlightCalls(t *testing.T) {
	dir := t.TempDir()
	if err := saveConfig(dir, helperConfig(t)); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	exports, cleanup := gateway(t, dir)
	runDiscovery(t, exports)
	cleanup()

	exports, cleanup = gateway(t, dir)
	defer cleanup()

	var slow tool.Tool
	for _, candidate := range exports.Tools {
		if candidate.Definition().Name == "mcp_helper_slow" {
			slow = candidate
		}
	}
	if slow == nil {
		t.Fatalf("tools = %v", toolNames(exports.Tools))
	}

	type outcome struct {
		result tool.Result
		err    error
	}
	finished := make(chan outcome, 1)
	go func() {
		result, err := slow.Invoke(context.Background(), tool.Invocation{
			Call: tool.Call{ID: "slow-1", Name: "mcp_helper_slow", Arguments: json.RawMessage(`{}`)},
		})
		finished <- outcome{result: result, err: err}
	}()

	// Retire the live session while the call is in flight.
	time.Sleep(100 * time.Millisecond)
	platformOf(t, exports).markStale()

	select {
	case got := <-finished:
		if got.err != nil {
			t.Fatalf("an in-flight call was aborted by reconfiguration: %v", got.err)
		}
		if text, ok := content.TextOnly(got.result.Content); !ok || text != "slow-done" {
			t.Fatalf("result = %q", text)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the in-flight call never finished")
	}
}

// platformOf recovers the plugin's platform from its exported tools. It is the
// test-only seam for behaviours that are not reachable through tool.Tool.
func platformOf(t *testing.T, exports Exports) *platform {
	t.Helper()
	for _, entry := range exports.Tools {
		switch value := entry.(type) {
		case *mcpTool:
			return value.platform
		case *statusTool:
			return value.platform
		}
	}
	t.Fatal("no exported tool exposes the platform")
	return nil
}

// TestSlotResizeDoesNotExceedTheLimit covers a configuration change that lowers
// max_concurrent_calls while calls are running: the limiter must not be
// replaced by a second one, which would allow holders plus new callers to exceed
// the configured limit.
func TestSlotResizeDoesNotExceedTheLimit(t *testing.T) {
	slots := &serverSlots{}
	releaseWide, err := slots.acquire(context.Background(), 4)
	if err != nil {
		t.Fatalf("acquireSlot: %v", err)
	}

	granted := make(chan struct{})
	go func() {
		release, err := slots.acquire(context.Background(), 1)
		if err == nil {
			release()
			close(granted)
		}
	}()

	select {
	case <-granted:
		t.Fatal("a lowered limit was bypassed by a second limiter")
	case <-time.After(100 * time.Millisecond):
	}

	releaseWide()
	select {
	case <-granted:
	case <-time.After(2 * time.Second):
		t.Fatal("the waiting call never proceeded")
	}
	if slots.inUse != 0 {
		t.Fatalf("limiter state leaked: in use = %d", slots.inUse)
	}
}
