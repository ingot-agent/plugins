package toolask

import (
	"context"
	"strings"
	"testing"

	"github.com/ingot-agent/sdk/content"
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

func TestSetupAppliesToRunningAskTool(t *testing.T) {
	binder := &fakeChannel{}
	exports, _, err := New(context.Background(), Dependencies{Interaction: binder, State: testStateScope{dir: writeTestConfig(t, Config{})}})
	if err != nil {
		t.Fatal(err)
	}
	channel := &configTestChannel{values: []interaction.Answer{
		{Name: "max_prompt_bytes", Value: interaction.IntegerValue(3)},
		{Name: "max_response_bytes", Value: interaction.IntegerValue(32)},
		{Name: "max_options", Value: interaction.IntegerValue(2)},
		{Name: "max_options_bytes", Value: interaction.IntegerValue(32)},
	}}
	result, err := exports.Operations[0].Invoke(context.Background(), operation.Request{Interaction: channel})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result.Output), `"restart_required":false`) {
		t.Fatalf("output = %s", result.Output)
	}
	toolResult, err := exports.Tools[0].Invoke(context.Background(), tool.Invocation{Call: tool.Call{Name: "ask_user", Arguments: []byte(`{"prompt":"four"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	text, _ := content.TextOnly(toolResult.Content)
	if !strings.Contains(text, ErrPromptLimit.Error()) {
		t.Fatalf("result = %q", text)
	}
}
