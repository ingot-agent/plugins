package toolmcp

import (
	"context"
	"sync"

	ingotabi "github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/httpx"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/logging"
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
