package usagedefault

import (
	"context"
	"testing"

	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/operation"
)

type setupChannel struct {
	request interaction.Request
}

func (c *setupChannel) Request(_ context.Context, request interaction.Request) (interaction.Response, error) {
	c.request = request
	answers := make([]interaction.Answer, 0, len(request.Fields))
	for _, field := range request.Fields {
		if field.Default != nil {
			answers = append(answers, interaction.Answer{Name: field.Name, Value: *field.Default})
		}
	}
	return interaction.Response{Values: answers}, nil
}

func (*setupChannel) Emit(context.Context, interaction.Event) error { return nil }
func (*setupChannel) Set(context.Context, interaction.State) error  { return nil }
func (*setupChannel) Clear(context.Context, string) error           { return nil }

func TestSetupSuggestsInjectedProvidersWithoutClosingFutureRoutes(t *testing.T) {
	current := validConfig()
	scope := testStateScope{dir: writeTestConfig(t, current)}
	active := cloneConfig(current)
	active.CacheEntries = defaultCacheEntries
	channel := &setupChannel{}
	op := &setupOperation{scope: scope, providerNames: []string{"first", "second"}, active: active}
	if _, err := op.Invoke(context.Background(), operation.Request{Interaction: channel}); err != nil {
		t.Fatal(err)
	}
	routes := findSetupField(t, channel.request.Fields, "routes")
	provider := findSetupField(t, routes.Element.Fields, "provider")
	if provider.Kind != interaction.FieldString || len(provider.Options) != 2 || provider.Options[0].Value != "first" || provider.Options[1].Value != "second" {
		t.Fatalf("provider field = %#v", provider)
	}
}

func findSetupField(t *testing.T, fields []interaction.Field, name string) interaction.Field {
	t.Helper()
	for _, field := range fields {
		if field.Name == name {
			return field
		}
	}
	t.Fatalf("field %q not found", name)
	return interaction.Field{}
}
