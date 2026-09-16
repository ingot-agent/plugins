package contextcompact

import (
	"context"
	"errors"
	"testing"

	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/operation"
)

type setupChannel struct {
	request  interaction.Request
	response interaction.Response
}

func (c *setupChannel) Request(_ context.Context, request interaction.Request) (interaction.Response, error) {
	c.request = request
	return c.response, nil
}

func (*setupChannel) Emit(context.Context, interaction.Event) error { return nil }
func (*setupChannel) Set(context.Context, interaction.State) error  { return nil }
func (*setupChannel) Clear(context.Context, string) error           { return nil }

func TestSetupUsesClosedProviderOptionsAndRejectsUnknownProvider(t *testing.T) {
	scope := testStateScope{dir: writeTestConfig(t, Config{})}
	active, err := normalizeConfigForProviders(Config{}, []string{"first", "second"})
	if err != nil {
		t.Fatal(err)
	}
	channel := &setupChannel{response: interaction.Response{Values: []interaction.Answer{
		{Name: "provider", Value: interaction.StringValue("missing")},
	}}}
	op := &setupOperation{scope: scope, providerNames: []string{"first", "second"}, active: active}
	_, err = op.Invoke(context.Background(), operation.Request{Interaction: channel})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error = %v, want ErrInvalidConfig", err)
	}
	provider := findSetupField(t, channel.request.Fields, "provider")
	if provider.Kind != interaction.FieldChoice || len(provider.Options) != 3 || provider.Options[0].Value != "" || provider.Options[1].Value != "first" || provider.Options[2].Value != "second" {
		t.Fatalf("provider field = %#v", provider)
	}
	stored, err := loadConfig(scope.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if stored != (Config{}) {
		t.Fatalf("stored = %#v, want empty", stored)
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
