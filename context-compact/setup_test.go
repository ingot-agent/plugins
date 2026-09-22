package contextcompact

import (
	"context"
	"errors"
	"testing"

	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/operation"
)

type setupChannel struct {
	request   interaction.Request
	response  interaction.Response
	onRequest func(interaction.Request)
}

func (c *setupChannel) Request(_ context.Context, request interaction.Request) (interaction.Response, error) {
	c.request = request
	if c.onRequest != nil {
		c.onRequest(request)
	}
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
	op := &setupOperation{scope: scope, providerSources: []model.ProviderSource{&setupProviderSource{names: []string{"first", "second"}}}, compactor: setupCompactor(active)}
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

func TestSetupRefreshesProvidersAndRepairsRemovedSelection(t *testing.T) {
	scope := testStateScope{dir: writeTestConfig(t, Config{Provider: "removed"})}
	source := &setupProviderSource{}
	op := &setupOperation{scope: scope, providerSources: []model.ProviderSource{source}, compactor: setupCompactor(normalizedConfig{})}
	for _, selected := range []string{"", "added"} {
		if selected != "" {
			source.names = []string{selected}
		}
		channel := &setupChannel{
			response: interaction.Response{Values: []interaction.Answer{{Name: "provider", Value: interaction.StringValue(selected)}}},
			onRequest: func(request interaction.Request) {
				field := findSetupField(t, request.Fields, "provider")
				if field.Kind != interaction.FieldChoice || len(field.Options) != len(source.names)+1 {
					t.Fatalf("provider options = %#v", field)
				}
				if selected == "" && field.Default != nil {
					t.Fatalf("removed provider must not remain a choice default: %#v", field.Default)
				}
				if selected != "" && field.Options[1].Value != selected {
					t.Fatalf("new provider absent from options: %#v", field)
				}
			},
		}
		if _, err := op.Invoke(context.Background(), operation.Request{Interaction: channel}); err != nil {
			t.Fatal(err)
		}
		if running := op.compactor.config.Load(); running.provider != selected {
			t.Fatalf("running provider = %q, want %q", running.provider, selected)
		}
		stored, err := loadConfig(scope.Dir())
		if err != nil || stored.Provider != selected {
			t.Fatalf("saved provider = %q, error = %v", stored.Provider, err)
		}
	}
}

func TestSetupRejectsProviderRemovedDuringInteraction(t *testing.T) {
	for _, remaining := range [][]string{nil, {"other"}} {
		scope := testStateScope{dir: writeTestConfig(t, Config{})}
		source := &setupProviderSource{names: []string{"selected"}}
		selected := "selected"
		channel := &setupChannel{
			response:  interaction.Response{Values: []interaction.Answer{{Name: "provider", Value: interaction.StringValue(selected)}}},
			onRequest: func(interaction.Request) { source.names = remaining },
		}
		op := &setupOperation{scope: scope, providerSources: []model.ProviderSource{source}, compactor: setupCompactor(normalizedConfig{})}
		if _, err := op.Invoke(context.Background(), operation.Request{Interaction: channel}); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("error = %v, want ErrInvalidConfig", err)
		}
		stored, err := loadConfig(scope.Dir())
		if err != nil || stored.Provider != "" {
			t.Fatalf("removed selection was saved: %#v, error = %v", stored, err)
		}
	}
}

func setupCompactor(configuration normalizedConfig) *compactor {
	instance := &compactor{}
	instance.config.Store(&configuration)
	return instance
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
