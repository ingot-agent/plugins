package toolruntime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/operation"
	"github.com/ingot-agent/sdk/tool"
)

type configTestChannel struct{ values []interaction.Answer }

func (c *configTestChannel) Request(context.Context, interaction.Request) (interaction.Response, error) {
	return interaction.Response{Values: c.values}, nil
}
func (*configTestChannel) Emit(context.Context, interaction.Event) error { return nil }
func (*configTestChannel) Set(context.Context, interaction.State) error  { return nil }
func (*configTestChannel) Clear(context.Context, string) error           { return nil }

func TestSetupAppliesToRunningToolRuntime(t *testing.T) {
	candidate := &fakeTool{definition: tool.Definition{Name: "echo", Description: "echo", InputSchema: []byte(`{"type":"object"}`)}}
	exports, _, err := New(context.Background(), Dependencies{Tools: []tool.Tool{candidate}, State: testStateScope{dir: writeTestConfig(t, Config{})}})
	if err != nil {
		t.Fatal(err)
	}
	channel := &configTestChannel{values: []interaction.Answer{
		{Name: "max_arguments_bytes", Value: interaction.IntegerValue(2)},
		{Name: "max_text_bytes", Value: interaction.IntegerValue(32)},
		{Name: "max_inline_part_bytes", Value: interaction.IntegerValue(32)},
		{Name: "max_inline_bytes", Value: interaction.IntegerValue(64)},
	}}
	result, err := exports.Operations[0].Invoke(context.Background(), operation.Request{Interaction: channel})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result.Output), `"restart_required":false`) {
		t.Fatalf("output = %s", result.Output)
	}
	_, err = exports.Runtime.Call(context.Background(), tool.Invocation{Call: tool.Call{Name: "echo", Arguments: []byte(`{"x":1}`)}})
	if !errors.Is(err, tool.ErrInvalidArguments) {
		t.Fatalf("error = %v, want ErrInvalidArguments", err)
	}
}
