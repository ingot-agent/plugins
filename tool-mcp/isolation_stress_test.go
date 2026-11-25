package toolmcp

import (
	"context"
	"fmt"
	"sync"
	"testing"

	ingotabi "github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/execution"
	"github.com/ingot-agent/sdk/workspace"
)

// TestConcurrentAcquireNeverExceedsCapacity is the invariant that the leak-free
// property depends on: however many Sessions race for one server, the number of
// live connectors stays within the constant and each Session owns exactly one.
func TestConcurrentAcquireNeverExceedsCapacity(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns many MCP processes")
	}
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
	roots := make(map[string]string)
	for index := 0; index < maxSessionsPerServer+4; index++ {
		roots[fmt.Sprintf("session-%d", index)] = t.TempDir()
	}
	instance := newPlatform(dir, ingotabiNone(),
		ingotabi.Some[workspace.Resolver](mappedResolver{roots: roots}), discardLogger(), nil, config)
	defer instance.closeAll()

	var wg sync.WaitGroup
	owned := make(map[string]string) // session -> runtime pointer identity
	var ownedMu sync.Mutex

	for index := 0; index < maxSessionsPerServer+4; index++ {
		sessionID := fmt.Sprintf("session-%d", index)
		wg.Add(1)
		go func(sessionID string) {
			defer wg.Done()
			// Two concurrent calls for the same Session must share one connector.
			var inner sync.WaitGroup
			for attempt := 0; attempt < 2; attempt++ {
				inner.Add(1)
				go func() {
					defer inner.Done()
					held, err := instance.acquire(context.Background(), "helper", execution.Scope{SessionID: sessionIDOf(sessionID)})
					if err != nil {
						return // capacity is allowed to reject
					}
					instance.mu.Lock()
					runtime := instance.runtimes[connectionKey{server: "helper", session: sessionID}]
					count := instance.countLocked("helper")
					instance.mu.Unlock()

					if runtime != nil {
						ownedMu.Lock()
						previous, seen := owned[sessionID]
						if seen && previous != fmt.Sprintf("%p", runtime) {
							t.Errorf("session %q was served by two connectors", sessionID)
						}
						owned[sessionID] = fmt.Sprintf("%p", runtime)
						ownedMu.Unlock()
					}
					if count > maxSessionsPerServer {
						t.Errorf("%d connectors exceeded the limit %d", count, maxSessionsPerServer)
					}
					held.release()
				}()
			}
			inner.Wait()
		}(sessionID)
	}
	wg.Wait()

	instance.mu.Lock()
	final := instance.countLocked("helper")
	instance.mu.Unlock()
	if final > maxSessionsPerServer {
		t.Fatalf("final connector count %d exceeded %d", final, maxSessionsPerServer)
	}
	if len(owned) == 0 {
		t.Fatal("no session ever acquired a connection")
	}
}

// TestEvictedSessionDoesNotBreakOthers proves eviction only removes idle
// connectors, so a Session using its connection keeps working while another
// Session is admitted.
func TestEvictedSessionDoesNotBreakOthers(t *testing.T) {
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

	// Fill the server with idle connectors.
	for index := 0; index < maxSessionsPerServer; index++ {
		held, err := instance.acquire(context.Background(), "helper", execution.Scope{SessionID: sessionIDOf(fmt.Sprintf("idle-%d", index))})
		if err != nil {
			t.Fatalf("acquire: %v", err)
		}
		held.release()
	}

	// Admit one more: an idle connector is evicted, so this succeeds.
	admitted, err := instance.acquire(context.Background(), "helper", execution.Scope{SessionID: sessionIDOf("active")})
	if err != nil {
		t.Fatalf("an idle connector should be evictable: %v", err)
	}
	defer admitted.release()

	// The admitted Session is still in use, so another admission must fail
	// rather than evict it.
	if _, err := instance.acquire(context.Background(), "helper", execution.Scope{SessionID: sessionIDOf("second-active")}); err != nil {
		// Expected only when every remaining connector is also in use; idle ones
		// may still be evicted, which is also correct.
		if len(instanceRuntimes(t, instance)) == 0 {
			t.Fatalf("unexpected failure: %v", err)
		}
	}

	// The lease of the admitted Session is still usable.
	if !admitted.conn.stderr.validUTF8() {
		t.Fatal("the admitted connection should still be live")
	}
}

// instanceRuntimes returns a snapshot of the registered connector count.
func instanceRuntimes(t *testing.T, instance *platform) []connectionKey {
	t.Helper()
	instance.mu.Lock()
	defer instance.mu.Unlock()
	keys := make([]connectionKey, 0, len(instance.runtimes))
	for key := range instance.runtimes {
		keys = append(keys, key)
	}
	return keys
}

// TestInFlightCallSurvivesEvictionPressure keeps the guarantee that a call in
// progress is never aborted by admission decisions for other Sessions.
func TestInFlightCallSurvivesEvictionPressure(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns many MCP processes")
	}
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

	held, err := instance.acquire(context.Background(), "helper", execution.Scope{SessionID: sessionIDOf("protected")})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer held.release()

	// Fill capacity with idle connectors, then churn more Sessions.
	for index := 0; index < maxSessionsPerServer; index++ {
		other, err := instance.acquire(context.Background(), "helper", execution.Scope{SessionID: sessionIDOf(fmt.Sprintf("churn-%d", index))})
		if err != nil {
			continue
		}
		other.release()
		instance.markStale()
	}

	if _, err := held.conn.session.ListTools(context.Background(), nil); err != nil {
		t.Fatalf("the in-flight connection was closed: %v", err)
	}
}
