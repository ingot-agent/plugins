package toolmcp

import (
	"context"
	"sync"
	"testing"

	ingotabi "github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/execution"
	"github.com/ingot-agent/sdk/httpx"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/logging"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/workspace"
)

// testScope is a minimal state.Scope for tests.
type testScope string

func (s testScope) Dir() string { return string(s) }

// scriptedChannel answers interaction requests from a fixed map and records
// every request it received.
type scriptedChannel struct {
	mu       sync.Mutex
	answers  map[string]interaction.Value
	requests []interaction.Request
	// duringRequest runs while the host is supposedly collecting the answer,
	// which is where a concurrent configuration write happens.
	duringRequest func()
}

func newScriptedChannel(answers map[string]interaction.Value) *scriptedChannel {
	return &scriptedChannel{answers: answers}
}

func (c *scriptedChannel) Request(_ context.Context, request interaction.Request) (interaction.Response, error) {
	c.mu.Lock()
	c.requests = append(c.requests, request)
	hook := c.duringRequest
	c.mu.Unlock()
	if hook != nil {
		hook()
	}
	response := interaction.Response{}
	for _, field := range request.Fields {
		value, ok := c.answers[field.Name]
		if !ok {
			continue
		}
		response.Values = append(response.Values, interaction.Answer{Name: field.Name, Value: value})
	}
	return response, nil
}

func (*scriptedChannel) Emit(context.Context, interaction.Event) error { return nil }
func (*scriptedChannel) Set(context.Context, interaction.State) error  { return nil }
func (*scriptedChannel) Clear(context.Context, string) error           { return nil }

func boolPointer(value bool) *bool { return &value }

func objectValue(entries map[string]interaction.Value) interaction.Value {
	list := make([]interaction.Entry, 0, len(entries))
	for name, value := range entries {
		list = append(list, interaction.Entry{Name: name, Value: value})
	}
	return interaction.ObjectValue(list)
}

// ingotabiNone is a typed empty HTTP capability for platform construction in
// tests that never use the http transport.
func ingotabiNone() ingotabi.Optional[httpx.Client] {
	return ingotabi.None[httpx.Client]()
}

func discardLogger() logging.Logger { return logging.Discard() }

// sessionIDOf converts a literal into a Session ID.
func sessionIDOf(value string) session.ID { return session.ID(value) }

// testScopeExecution builds an execution scope for tests that need a Session
// identity.
func testScopeExecution(sessionID string) execution.Scope {
	return execution.Scope{SessionID: session.ID(sessionID)}
}

// staticResolver binds every Session to one root.
type staticResolver struct {
	root string
	err  error
}

func (r staticResolver) Resolve(context.Context, execution.Scope) (workspace.Binding, error) {
	if r.err != nil {
		return workspace.Binding{}, r.err
	}
	return workspace.Binding{Root: r.root}, nil
}

// mustRuntime fetches a connector, failing the test if capacity was reached.
func mustRuntime(t *testing.T, instance *platform, key connectionKey) *serverRuntime {
	t.Helper()
	server, ok := instance.currentConfig().server(key.server)
	if !ok {
		t.Fatalf("server %q is not configured", key.server)
	}
	runtime, err := instance.runtimeFor(key, server, "", "")
	if err != nil {
		t.Fatalf("runtimeFor: %v", err)
	}
	return runtime
}

// validUTF8 reports whether the connection still has a usable transport. It is
// a cheap liveness probe for tests that must not call the server.
func (b *ringBuffer) validUTF8() bool { return b != nil }
