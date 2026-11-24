package toolmcp

import (
	"context"
	"sync"

	"github.com/ingot-agent/sdk/interaction"
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
}

func newScriptedChannel(answers map[string]interaction.Value) *scriptedChannel {
	return &scriptedChannel{answers: answers}
}

func (c *scriptedChannel) Request(_ context.Context, request interaction.Request) (interaction.Response, error) {
	c.mu.Lock()
	c.requests = append(c.requests, request)
	c.mu.Unlock()
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
