package toolmcp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	ingotabi "github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/execution"
	"github.com/ingot-agent/sdk/tool"
	"github.com/ingot-agent/sdk/workspace"
)

// whereReport is the payload of the helper's "where" tool.
type whereReport struct {
	Cwd   string `json:"cwd"`
	Nonce string `json:"nonce"`
}

// callWhere invokes the helper's location tool as one Session.
func callWhere(t *testing.T, entry tool.Tool, sessionID string) whereReport {
	t.Helper()
	result, err := entry.Invoke(context.Background(), tool.Invocation{
		Scope: execution.Scope{SessionID: sessionIDOf(sessionID)},
		Call:  tool.Call{ID: "where-1", Name: "mcp_helper_where", Arguments: json.RawMessage(`{}`)},
	})
	if err != nil {
		t.Fatalf("where call for session %q: %v", sessionID, err)
	}
	text, ok := content.TextOnly(result.Content)
	if !ok {
		t.Fatalf("where result = %#v", result.Content)
	}
	var report whereReport
	if err := json.Unmarshal([]byte(text), &report); err != nil {
		t.Fatalf("decode where report %q: %v", text, err)
	}
	return report
}

// platformWithWorkspace builds a platform bound to resolver for one server.
func platformWithWorkspace(t *testing.T, dir string, resolver workspace.Resolver) *platform {
	t.Helper()
	config, err := normalizeConfig(helperConfig(t))
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	return newPlatform(dir, ingotabiNone(), ingotabi.Some[workspace.Resolver](resolver), discardLogger(), nil, config)
}

// TestCwdFollowsTheSessionWorkspace is the core promise: the MCP process is
// created inside the Session's workspace, so relative paths land there.
func TestCwdFollowsTheSessionWorkspace(t *testing.T) {
	dir := t.TempDir()
	if err := saveConfig(dir, helperConfig(t)); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	exports, cleanup := gateway(t, dir)
	runDiscovery(t, exports)
	cleanup()

	root := t.TempDir()
	instance := platformWithWorkspace(t, dir, staticResolver{root: root})
	defer instance.closeAll()

	held, err := instance.acquire(context.Background(), "helper", execution.Scope{SessionID: "session-a"})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer held.release()
	_ = held

	runtime, ok := instance.runtimes[connectionKey{server: "helper", session: "session-a"}]
	if !ok {
		t.Fatal("no connection was registered for the session")
	}
	if runtime.root != root {
		t.Fatalf("connection root = %q, want %q", runtime.root, root)
	}
	if runtime.cwdSource != cwdSourceWorkspace {
		t.Fatalf("cwd_source = %q", runtime.cwdSource)
	}
}

// TestSessionsGetIsolatedProcesses proves isolation end to end: two Sessions
// reach two different child processes, each in its own workspace.
func TestSessionsGetIsolatedProcesses(t *testing.T) {
	dir := t.TempDir()
	if err := saveConfig(dir, helperConfig(t)); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	exports, cleanup := gateway(t, dir)
	runDiscovery(t, exports)
	cleanup()

	// Two workspaces, one resolver that maps a Session to its own root.
	roots := map[string]string{
		"session-a": t.TempDir(),
		"session-b": t.TempDir(),
	}
	rootB, err := filepath.EvalSymlinks(roots["session-b"])
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	config, err := normalizeConfig(helperConfig(t))
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	instance := newPlatform(dir, ingotabiNone(),
		ingotabi.Some[workspace.Resolver](mappedResolver{roots: roots}), discardLogger(), nil, config)
	defer instance.closeAll()

	where := &mcpTool{
		platform: instance,
		server:   "helper",
		wireName: "where",
		definition: tool.Definition{
			Name:        "mcp_helper_where",
			Description: "where",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
	}

	first := callWhere(t, where, "session-a")
	second := callWhere(t, where, "session-b")

	if first.Cwd == second.Cwd {
		t.Fatalf("both Sessions reported cwd %q", first.Cwd)
	}
	if first.Nonce == second.Nonce {
		t.Fatal("both Sessions were served by the same MCP process")
	}
	if second.Cwd != rootB {
		t.Fatalf("session-b cwd = %q, want %q", second.Cwd, rootB)
	}

	// Each Session returns to its own process rather than reconnecting.
	again := callWhere(t, where, "session-a")
	if again.Nonce != first.Nonce {
		t.Fatal("session-a was served by a different process on the second call")
	}
	if again.Cwd != first.Cwd {
		t.Fatalf("session-a cwd changed: %q -> %q", first.Cwd, again.Cwd)
	}
}

// TestUnassignedWorkspaceFailsClosed covers the case that would otherwise cache
// a connection in the wrong directory forever: assignment can happen after the
// first call, so guessing would be silently wrong.
func TestUnassignedWorkspaceFailsClosed(t *testing.T) {
	dir := t.TempDir()
	if err := saveConfig(dir, helperConfig(t)); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	exports, cleanup := gateway(t, dir)
	runDiscovery(t, exports)
	cleanup()

	instance := platformWithWorkspace(t, dir, staticResolver{err: workspace.ErrNotAssigned})
	defer instance.closeAll()

	_, err := instance.acquire(context.Background(), "helper", execution.Scope{SessionID: "session-a"})
	if !errors.Is(err, workspace.ErrNotAssigned) {
		t.Fatalf("error = %v, want workspace.ErrNotAssigned", err)
	}
	if len(instance.runtimes) != 0 {
		t.Fatal("a failed resolution must not register a connection")
	}
}

// TestServerCwdOverridesWorkspace keeps the explicit escape hatch working.
func TestServerCwdOverridesWorkspace(t *testing.T) {
	pinned := t.TempDir()
	config := helperConfig(t)
	config.Servers[0].Cwd = pinned
	config, err := normalizeConfig(config)
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	instance := newPlatform(t.TempDir(), ingotabiNone(),
		ingotabi.Some[workspace.Resolver](staticResolver{root: t.TempDir()}), discardLogger(), nil, config)
	defer instance.closeAll()

	if _, err := instance.acquire(context.Background(), "helper", execution.Scope{SessionID: "session-a"}); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	runtime := instance.runtimes[connectionKey{server: "helper", session: "session-a"}]
	if runtime.root != pinned || runtime.cwdSource != cwdSourceConfig {
		t.Fatalf("root = %q source = %q", runtime.root, runtime.cwdSource)
	}
}

// TestNoWorkspaceCapabilityInherits documents the one composition where no
// Session can ever be bound.
func TestNoWorkspaceCapabilityInherits(t *testing.T) {
	dir := t.TempDir()
	if err := saveConfig(dir, helperConfig(t)); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	exports, cleanup := gateway(t, dir)
	runDiscovery(t, exports)
	cleanup()

	config, err := normalizeConfig(helperConfig(t))
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	instance := newPlatform(dir, ingotabiNone(), ingotabi.None[workspace.Resolver](), discardLogger(), nil, config)
	defer instance.closeAll()

	if _, err := instance.acquire(context.Background(), "helper", execution.Scope{SessionID: "session-a"}); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	runtime := instance.runtimes[connectionKey{server: "helper", session: "session-a"}]
	if runtime.root != "" || runtime.cwdSource != cwdSourceProcess {
		t.Fatalf("root = %q source = %q, want the inherited runtime directory", runtime.root, runtime.cwdSource)
	}
}

// TestSessionCapacityFailsClosed proves the limit never turns into sharing.
func TestSessionCapacityFailsClosed(t *testing.T) {
	dir := t.TempDir()
	if err := saveConfig(dir, helperConfig(t)); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	exports, cleanup := gateway(t, dir)
	runDiscovery(t, exports)
	cleanup()

	config, err := normalizeConfig(helperConfig(t))
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	instance := newPlatform(dir, ingotabiNone(), ingotabi.None[workspace.Resolver](), discardLogger(), nil, config)
	defer instance.closeAll()

	var held []*lease
	for index := 0; index < maxSessionsPerServer; index++ {
		sessionID := sessionIDOf("session-" + string(rune('a'+index)))
		current, err := instance.acquire(context.Background(), "helper", execution.Scope{SessionID: sessionID})
		if err != nil {
			t.Fatalf("acquire %d: %v", index, err)
		}
		held = append(held, current)
	}
	// Every session is busy, so one more must fail rather than share.
	if _, err := instance.acquire(context.Background(), "helper", execution.Scope{SessionID: "session-overflow"}); err == nil {
		t.Fatal("exceeding the session limit must fail closed")
	}
	for _, current := range held {
		current.release()
	}
}

// TestIdleSessionIsEvictedForANewOne covers eviction: a finished Session yields
// its slot so another Session can connect.
func TestIdleSessionIsEvictedForANewOne(t *testing.T) {
	dir := t.TempDir()
	if err := saveConfig(dir, helperConfig(t)); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	exports, cleanup := gateway(t, dir)
	runDiscovery(t, exports)
	cleanup()

	config, err := normalizeConfig(helperConfig(t))
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	instance := newPlatform(dir, ingotabiNone(), ingotabi.None[workspace.Resolver](), discardLogger(), nil, config)
	defer instance.closeAll()

	for index := 0; index < maxSessionsPerServer; index++ {
		sessionID := sessionIDOf("filler-" + string(rune('a'+index)))
		current, err := instance.acquire(context.Background(), "helper", execution.Scope{SessionID: sessionID})
		if err != nil {
			t.Fatalf("acquire filler %d: %v", index, err)
		}
		current.release()
	}

	extra, err := instance.acquire(context.Background(), "helper", execution.Scope{SessionID: "session-extra"})
	if err != nil {
		t.Fatalf("an idle session should have been evicted: %v", err)
	}
	extra.release()

	instance.mu.Lock()
	count := instance.countLocked("helper")
	_, present := instance.runtimes[connectionKey{server: "helper", session: "session-extra"}]
	instance.mu.Unlock()
	if count > maxSessionsPerServer {
		t.Fatalf("%d connections exceeded the limit %d", count, maxSessionsPerServer)
	}
	if !present {
		t.Fatal("the new session was not registered")
	}
}

// TestStatusReportsConnectionsAndCwdSource makes the working-directory decision
// observable, which is how "my files landed elsewhere" gets answered.
func TestStatusReportsConnectionsAndCwdSource(t *testing.T) {
	dir := t.TempDir()
	if err := saveConfig(dir, helperConfig(t)); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	exports, cleanup := gateway(t, dir)
	runDiscovery(t, exports)
	cleanup()

	instance := platformWithWorkspace(t, dir, staticResolver{root: t.TempDir()})
	defer instance.closeAll()

	first, err := instance.acquire(context.Background(), "helper", execution.Scope{SessionID: "session-a"})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer first.release()
	second, err := instance.acquire(context.Background(), "helper", execution.Scope{SessionID: "session-b"})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer second.release()

	statuses := instance.statuses()
	if len(statuses) != 1 {
		t.Fatalf("statuses = %#v", statuses)
	}
	if statuses[0].Connections != 2 {
		t.Fatalf("connections = %d, want 2", statuses[0].Connections)
	}
	if statuses[0].CwdSource != cwdSourceWorkspace {
		t.Fatalf("cwd_source = %q", statuses[0].CwdSource)
	}
	if statuses[0].Status != "connected" {
		t.Fatalf("status = %q", statuses[0].Status)
	}
}

// TestDiscoveryUsesAPrivateSentinelDirectory keeps discovery out of user
// workspaces and out of the Runtime working directory.
func TestDiscoveryUsesAPrivateSentinelDirectory(t *testing.T) {
	instance := testPlatform(t, helperConfig(t))
	dir, err := instance.discoveryDir()
	if err != nil {
		t.Fatalf("discoveryDir: %v", err)
	}
	if filepath.Dir(dir) != instance.stateDir {
		t.Fatalf("discovery dir %q is not inside the state scope %q", dir, instance.stateDir)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat discovery dir: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("discovery dir is not a directory")
	}
}

// mappedResolver resolves each Session to its own root.
type mappedResolver struct {
	roots map[string]string
}

func (r mappedResolver) Resolve(_ context.Context, scope execution.Scope) (workspace.Binding, error) {
	root, ok := r.roots[string(scope.SessionID)]
	if !ok {
		return workspace.Binding{}, workspace.ErrNotAssigned
	}
	return workspace.Binding{Root: root}, nil
}
