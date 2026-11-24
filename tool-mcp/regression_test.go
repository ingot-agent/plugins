package toolmcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	ingotabi "github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/httpx"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/logging"
	"github.com/ingot-agent/sdk/operation"
	"github.com/ingot-agent/sdk/tool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestCrashedServerReconnects is the regression for a dead session that was
// never retired: after the child exits, the next call must reconnect instead of
// failing forever, and the outage must be visible in the status.
func TestCrashedServerReconnects(t *testing.T) {
	dir := t.TempDir()
	if err := saveConfig(dir, helperConfig(t)); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	exports, cleanup := gateway(t, dir)
	runDiscovery(t, exports)
	cleanup()

	config := helperConfig(t)
	config.Servers[0].Command = "definitely-not-a-real-executable"
	// Keep the same name so the snapshot tools stay exported.
	if err := saveConfig(dir, config); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}

	exports, cleanup = gateway(t, dir)
	defer cleanup()

	var echo tool.Tool
	for _, candidate := range exports.Tools {
		if candidate.Definition().Name == "mcp_helper_echo" {
			echo = candidate
		}
	}
	if echo == nil {
		t.Fatalf("tools = %v", toolNames(exports.Tools))
	}

	instance := platformOf(t, exports)
	_, err := echo.Invoke(context.Background(), tool.Invocation{
		Call: tool.Call{ID: "c1", Name: "mcp_helper_echo", Arguments: json.RawMessage(`{"text":"x"}`)},
	})
	if err == nil {
		t.Fatal("expected the call to fail")
	}
	statuses := instance.statuses()
	if len(statuses) != 1 {
		t.Fatalf("statuses = %#v", statuses)
	}
	if statuses[0].ConsecutiveErrors == 0 {
		t.Fatal("a failed connection must be recorded for backoff and status")
	}
	if statuses[0].Connected {
		t.Fatal("a failed connection must not report as connected")
	}
	if statuses[0].Status != "backoff" {
		t.Fatalf("status = %q", statuses[0].Status)
	}
}

// TestInvalidateRetiresDeadSession covers the transport-failure path directly.
func TestInvalidateRetiresDeadSession(t *testing.T) {
	runtime := &serverRuntime{name: "demo"}
	held := newLease(&connection{}, nil)
	runtime.session = held
	if !runtime.invalidate(held, errors.New("connection reset")) {
		t.Fatal("the live session should have been detached")
	}
	if runtime.session != nil {
		t.Fatal("a dead session must not stay attached")
	}
	if held.acquireRef() {
		t.Fatal("a retired lease must not accept new users")
	}
	// The failure is recorded so the next call backs off instead of re-dialling
	// on every call, and status reports the outage.
	runtime.mu.Lock()
	failures, retryAt, lastError := runtime.failures, runtime.retryAt, runtime.lastError
	runtime.mu.Unlock()
	if failures != 1 || retryAt.IsZero() || lastError == "" {
		t.Fatalf("failure state not recorded: failures=%d retryAt=%v lastError=%q", failures, retryAt, lastError)
	}
	status := runtime.status(serverStatus{Name: "demo"})
	if status.Status != "backoff" || status.Connected || status.ConsecutiveErrors != 1 {
		t.Fatalf("status = %#v", status)
	}

	// The call that held the lease still releases it; a retired lease closes the
	// connection at that point.
	held.release()
	if runtime.invalidate(held, errors.New("connection reset")) {
		t.Fatal("a detached session must not be reported as live")
	}
}

// TestRestartRequiredComparesWithTheRunningImage is the regression for a
// snapshot-relative answer: after a refresh that is not followed by a restart, a
// second refresh must still report restart_required.
func TestRestartRequiredComparesWithTheRunningImage(t *testing.T) {
	dir := t.TempDir()
	if err := saveConfig(dir, helperConfig(t)); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	exports, cleanup := gateway(t, dir)
	runDiscovery(t, exports)
	cleanup()

	// Start an image that exports the snapshot, then change the filter so the
	// next discovery differs from the running image.
	exports, cleanup = gateway(t, dir)
	defer cleanup()

	stored, err := loadConfig(dir)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	storedConfig, err := normalizeConfig(stored)
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	server := storedConfig.Servers[0]
	server.DisabledTools = []string{"fail"}
	channel := newScriptedChannel(map[string]interaction.Value{
		"refresh_now": interaction.BooleanValue(true),
		"servers":     interaction.ListValue([]interaction.Value{serverValue(server)}),
	})
	configOperation := findOperation(t, exports.Operations, setupOperationName)
	first, err := configOperation.Invoke(context.Background(), operation.Request{Interaction: channel})
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	var firstOutput setupOutput
	if err := json.Unmarshal(first.Output, &firstOutput); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if !firstOutput.RestartRequired {
		t.Fatal("the first change must require a restart")
	}

	// No restart happened: the same query must keep reporting true, because the
	// image still exports the old tool set.
	refresh := findOperation(t, exports.Operations, refreshOperationName)
	refreshed, err := refresh.Invoke(context.Background(), operation.Request{Interaction: interaction.Unavailable()})
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	var refreshResult refreshOutput
	if err := json.Unmarshal(refreshed.Output, &refreshResult); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if !refreshResult.RestartRequired {
		t.Fatal("a refresh without a restart must still report restart_required")
	}
	if len(refreshResult.Added) != 0 || len(refreshResult.Removed) != 0 {
		t.Fatalf("the snapshot did not change again: added=%v removed=%v",
			refreshResult.Added, refreshResult.Removed)
	}
}

// TestCancelledDiscoveryDoesNotPersist covers the cancellation contract: a
// cancelled refresh must neither save a truncated snapshot nor report success.
func TestCancelledDiscoveryDoesNotPersist(t *testing.T) {
	dir := t.TempDir()
	if err := saveConfig(dir, helperConfig(t)); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	exports, cleanup := gateway(t, dir)
	runDiscovery(t, exports)
	cleanup()

	before, err := loadSnapshot(dir)
	if err != nil {
		t.Fatalf("loadSnapshot: %v", err)
	}

	exports, cleanup = gateway(t, dir)
	defer cleanup()
	refresh := findOperation(t, exports.Operations, refreshOperationName)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := refresh.Invoke(ctx, operation.Request{Interaction: interaction.Unavailable()}); err == nil {
		t.Fatal("a cancelled refresh must fail")
	}

	after, err := loadSnapshot(dir)
	if err != nil {
		t.Fatalf("loadSnapshot: %v", err)
	}
	if got, want := after.toolNames(), before.toolNames(); !equalStringSets(got, want) {
		t.Fatalf("a cancelled discovery changed the snapshot: %v -> %v", want, got)
	}
}

// TestKeepDoesNotDiscardSubmittedSecrets covers a rename: "keep" with no stored
// entry for the new name must not throw away what the user just typed, and a
// masked blank entry must not be stored as an empty value.
func TestKeepDoesNotDiscardSubmittedSecrets(t *testing.T) {
	current, err := normalizeConfig(Config{Servers: []ServerConfig{{
		Name: "github", Command: "true", Env: map[string]string{"TOKEN": "stored"},
	}}})
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}

	renamed := objectValue(map[string]interaction.Value{
		"name":           interaction.StringValue("gh"),
		"transport":      interaction.StringValue(transportStdio),
		"command":        interaction.StringValue("true"),
		"env":            interaction.StringsValue([]string{"TOKEN=typed-now", "BLANK="}),
		"secrets_action": interaction.StringValue(secretActionKeep),
	})
	server, err := parseServerValue(renamed, current)
	if err != nil {
		t.Fatalf("parseServerValue: %v", err)
	}
	if server.Env["TOKEN"] != "typed-now" {
		t.Fatalf("submitted secret was discarded on rename: %#v", server.Env)
	}
	if _, exists := server.Env["BLANK"]; exists {
		t.Fatalf("a masked blank entry was stored as an empty secret: %#v", server.Env)
	}
}

// TestRedactSecrets hides configured credentials in server-controlled text.
func TestRedactSecrets(t *testing.T) {
	server := ServerConfig{
		Name: "demo",
		Env:  map[string]string{"TOKEN": "super-secret-token"},
		Headers: map[string]string{
			"Authorization": "Bearer header-secret",
		},
	}
	message := "auth failed for super-secret-token (Bearer header-secret)"
	redacted := redactSecrets(server, message)
	for _, secret := range []string{"super-secret-token", "Bearer header-secret"} {
		if containsSubstring(redacted, secret) {
			t.Fatalf("secret %q survived redaction: %q", secret, redacted)
		}
	}
}

func containsSubstring(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		(func() bool {
			for i := 0; i+len(needle) <= len(haystack); i++ {
				if haystack[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		})()
}

// TestShutdownTimeoutIsBounded keeps the transport escalation joinable.
func TestShutdownTimeoutIsBounded(t *testing.T) {
	if _, err := normalizeConfig(Config{Servers: []ServerConfig{{
		Name: "demo", Command: "true", ShutdownTimeoutSeconds: 60,
	}}}); err == nil {
		t.Fatal("an unbounded shutdown timeout must be rejected")
	}
	config, err := normalizeConfig(Config{Servers: []ServerConfig{{
		Name: "demo", Command: "true", ShutdownTimeoutSeconds: maxShutdownTimeout,
	}}})
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	if config.Servers[0].ShutdownTimeoutSeconds != maxShutdownTimeout {
		t.Fatalf("shutdown timeout = %d", config.Servers[0].ShutdownTimeoutSeconds)
	}
}

// TestResourceLinkRespectsTheBudget covers the one content kind that used to
// bypass max_result_bytes.
func TestResourceLinkRespectsTheBudget(t *testing.T) {
	uri := "file:///tmp/" + strings.Repeat("a", 64)
	callResult := mcp.CallToolResult{Content: []mcp.Content{
		&mcp.ResourceLink{URI: uri, Name: "big", MIMEType: "text/plain"},
	}}
	mapped := mapCallResult("demo", &callResult, 16)
	text, ok := content.TextOnly(mapped.Content)
	if !ok {
		t.Fatalf("content = %#v", mapped.Content)
	}
	if containsSubstring(text, uri) {
		t.Fatalf("an oversized resource link was emitted: %q", text)
	}
	if !containsSubstring(text, "truncated") {
		t.Fatalf("truncation notice missing: %q", text)
	}
}

// TestProgressIsBounded keeps server-controlled progress text out of the
// unbounded path.
func TestProgressIsBounded(t *testing.T) {
	long := ""
	for len(long) < 4*errorMessageLimit {
		long += "0123456789"
	}
	if got := sanitizeMessage(long); len(got) > errorMessageLimit+3 {
		t.Fatalf("progress text is %d bytes", len(got))
	}
	if got := sanitizeMessage("line one\nline two"); containsSubstring(got, "\n") {
		t.Fatalf("progress text was not flattened: %q", got)
	}
}

// TestConstructionRejectsNilCapabilities documents the fail-fast checks.
func TestConstructionRejectsNilCapabilities(t *testing.T) {
	cases := map[string]Dependencies{
		"nil state":  {},
		"nil logger": {State: testScope(t.TempDir()), Logger: ingotabi.Some[logging.Logger](nil)},
		"nil http":   {State: testScope(t.TempDir()), HTTP: ingotabi.Some[httpx.Client](nil)},
		"empty dir":  {State: testScope("")},
	}
	for name, deps := range cases {
		if _, _, err := New(context.Background(), deps); err == nil {
			t.Fatalf("%s: expected an error", name)
		}
	}
}

// TestCommitSnapshotRejectsStaleGeneration covers the guard that stops a
// discovery pass started before a reconfiguration from overwriting the newer
// snapshot.
func TestCommitSnapshotRejectsStaleGeneration(t *testing.T) {
	instance := testPlatform(t, Config{})
	stale := instance.currentGeneration()
	config := instance.currentConfig()
	config.ToolNamePrefix = "other"
	normalized, err := normalizeConfig(config)
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	instance.setConfig(normalized)

	snapshot := emptySnapshot("hash")
	if err := instance.commitSnapshot(context.Background(), snapshot, stale); !errors.Is(err, ErrConfigConflict) {
		t.Fatalf("error = %v, want ErrConfigConflict", err)
	}
	if _, err := loadSnapshot(instance.stateDir); err != nil {
		t.Fatalf("loadSnapshot: %v", err)
	}
	if err := instance.commitSnapshot(context.Background(), snapshot, instance.currentGeneration()); err != nil {
		t.Fatalf("a current snapshot must be persisted: %v", err)
	}
}

// TestAcquireAfterCleanupFails covers the terminal state: a call that arrives
// during shutdown must not start a new child process.
func TestAcquireAfterCleanupFails(t *testing.T) {
	instance := testPlatform(t, Config{Servers: []ServerConfig{{
		Name: "demo", Command: "true",
	}}})
	instance.closeAll()
	if _, err := instance.acquire(context.Background(), "demo"); err == nil {
		t.Fatal("a closed platform must refuse new sessions")
	}
}
