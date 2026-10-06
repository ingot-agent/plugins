package promptdefault

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/operation"
	"github.com/ingot-agent/sdk/prompt"
)

type liveTestScope struct{ dir string }

func (s liveTestScope) Dir() string { return s.dir }

type liveTestChannel struct{ response interaction.Response }

func (c *liveTestChannel) Request(context.Context, interaction.Request) (interaction.Response, error) {
	return c.response, nil
}
func (*liveTestChannel) Emit(context.Context, interaction.Event) error { return nil }
func (*liveTestChannel) Set(context.Context, interaction.State) error  { return nil }
func (*liveTestChannel) Clear(context.Context, string) error           { return nil }

func TestSetupAppliesToRunningRenderer(t *testing.T) {
	scope := liveTestScope{dir: filepath.Join(t.TempDir(), "state")}
	exports, _, err := New(context.Background(), Dependencies{State: scope})
	if err != nil {
		t.Fatal(err)
	}
	channel := &liveTestChannel{response: interaction.Response{Values: []interaction.Answer{
		{Name: "system_prompt", Value: interaction.StringValue("updated system")},
		{Name: "max_block_bytes", Value: interaction.IntegerValue(128)},
		{Name: "max_system_bytes", Value: interaction.IntegerValue(256)},
	}}}
	result, err := exports.Operations[0].Invoke(context.Background(), operation.Request{Interaction: channel})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result.Output), `"restart_required":false`) {
		t.Fatalf("output = %s", result.Output)
	}
	messages, err := exports.Renderer.Render(context.Background(), prompt.Request{Input: content.FromText("hello")})
	if err != nil {
		t.Fatal(err)
	}
	text, ok := content.TextOnly(messages[0].Content)
	if !ok || text != "updated system" {
		t.Fatalf("system message = %#v", messages[0])
	}
}
