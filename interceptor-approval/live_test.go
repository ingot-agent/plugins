package approval

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

func TestSetupAppliesToRunningApprovalInterceptor(t *testing.T) {
	exports, _, err := New(context.Background(), Dependencies{State: testStateScope{dir: writeTestConfig(t, Config{})}})
	if err != nil {
		t.Fatal(err)
	}
	channel := &configTestChannel{values: []interaction.Answer{
		{Name: "default_action", Value: interaction.StringValue(actionDeny)},
		{Name: "argument_display", Value: interaction.StringValue(displayNamesOnly)},
		{Name: "max_display_bytes", Value: interaction.IntegerValue(32)},
		{Name: "rules", Value: interaction.ListValue(nil)},
	}}
	result, err := exports.Operations[0].Invoke(context.Background(), operation.Request{Interaction: channel})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result.Output), `"restart_required":false`) {
		t.Fatalf("output = %s", result.Output)
	}
	called := false
	_, err = exports.Interceptors[0].Invoke(context.Background(), tool.Invocation{Call: tool.Call{Name: "blocked", Arguments: []byte(`{}`)}}, func(context.Context, tool.Invocation) (tool.Result, error) {
		called = true
		return tool.Result{}, nil
	})
	if !errors.Is(err, ErrApprovalDenied) || called {
		t.Fatalf("error = %v, called = %v", err, called)
	}
}
