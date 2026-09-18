package agentdefault

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/operation"
)

type setupTestScope struct{ dir string }

func (s setupTestScope) Dir() string { return s.dir }

type setupTestChannel struct {
	requests  []interaction.Request
	responses []interaction.Response
	onRequest func(interaction.Request)
}

func (c *setupTestChannel) Request(_ context.Context, request interaction.Request) (interaction.Response, error) {
	c.requests = append(c.requests, request)
	if c.onRequest != nil {
		c.onRequest(request)
	}
	response := c.responses[0]
	c.responses = c.responses[1:]
	return response, nil
}

func (*setupTestChannel) Emit(context.Context, interaction.Event) error { return nil }
func (*setupTestChannel) Set(context.Context, interaction.State) error  { return nil }
func (*setupTestChannel) Clear(context.Context, string) error           { return nil }

func TestSetupUsesProviderOptionsAndExplicitlyClearsOverrides(t *testing.T) {
	temperature, maxTokens := 0.7, 512
	current := Config{Provider: "first", Model: "model", Temperature: &temperature, MaxTokens: &maxTokens, MaxRounds: 10}
	active, err := normalizeConfig(current, []string{"first", "second"})
	if err != nil {
		t.Fatal(err)
	}
	scope := setupTestScope{dir: filepath.Join(t.TempDir(), "state")}
	if err := saveConfig(scope.Dir(), current); err != nil {
		t.Fatal(err)
	}
	channel := &setupTestChannel{responses: []interaction.Response{{Values: []interaction.Answer{
		{Name: "provider", Value: interaction.StringValue("second")},
		{Name: "model", Value: interaction.StringValue("next-model")},
		{Name: "temperature_mode", Value: interaction.StringValue(overrideInherit)},
		{Name: "max_tokens_mode", Value: interaction.StringValue(overrideInherit)},
		{Name: "max_rounds", Value: interaction.IntegerValue(12)},
	}}}}
	op := &setupOperation{scope: scope, providerSources: []model.ProviderSource{&setupProviderSource{names: []string{"first", "second"}}}, active: active}
	if _, err := op.Invoke(context.Background(), operation.Request{Interaction: channel}); err != nil {
		t.Fatal(err)
	}
	provider := findSetupField(t, channel.requests[0], "provider")
	if provider.Kind != interaction.FieldChoice || len(provider.Options) != 3 || provider.Options[0].Value != "" || provider.Options[2].Value != "second" {
		t.Fatalf("provider field = %#v", provider)
	}
	stored, err := loadConfig(scope.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if stored.Provider != "second" || stored.Model != "next-model" || stored.Temperature != nil || stored.MaxTokens != nil || stored.MaxRounds != 12 {
		t.Fatalf("stored = %#v", stored)
	}
	if len(channel.requests) != 1 {
		t.Fatalf("requests = %d, want one when both overrides inherit", len(channel.requests))
	}
}

func TestSetupRequestsOverrideValuesInASecondInteraction(t *testing.T) {
	current := Config{}
	active, err := normalizeConfig(current, nil)
	if err != nil {
		t.Fatal(err)
	}
	scope := setupTestScope{dir: filepath.Join(t.TempDir(), "state")}
	channel := &setupTestChannel{responses: []interaction.Response{
		{Values: []interaction.Answer{
			{Name: "temperature_mode", Value: interaction.StringValue(overrideSet)},
			{Name: "max_tokens_mode", Value: interaction.StringValue(overrideSet)},
			{Name: "max_rounds", Value: interaction.IntegerValue(defaultMaxRounds)},
		}},
		{Values: []interaction.Answer{
			{Name: "temperature", Value: interaction.NumberValue(0.25)},
			{Name: "max_tokens", Value: interaction.IntegerValue(256)},
		}},
	}}
	op := &setupOperation{scope: scope, active: active}
	if _, err := op.Invoke(context.Background(), operation.Request{Interaction: channel}); err != nil {
		t.Fatal(err)
	}
	if len(channel.requests) != 2 || channel.requests[1].Name != setupOperationName+".overrides" {
		t.Fatalf("requests = %#v", channel.requests)
	}
	stored, err := loadConfig(scope.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if stored.Temperature == nil || *stored.Temperature != 0.25 || stored.MaxTokens == nil || *stored.MaxTokens != 256 {
		t.Fatalf("stored = %#v", stored)
	}
}

func TestSetupRefreshesProvidersAndRepairsRemovedSelection(t *testing.T) {
	scope := testStateScope{dir: writeTestConfig(t, Config{Provider: "removed"})}
	source := &setupProviderSource{}
	op := &setupOperation{scope: scope, providerSources: []model.ProviderSource{source}}
	for _, selected := range []string{"", "added"} {
		if selected != "" {
			source.names = []string{selected}
		}
		channel := &setupTestChannel{
			responses: []interaction.Response{{Values: []interaction.Answer{
				{Name: "provider", Value: interaction.StringValue(selected)},
				{Name: "temperature_mode", Value: interaction.StringValue(overrideInherit)},
				{Name: "max_tokens_mode", Value: interaction.StringValue(overrideInherit)},
			}}},
			onRequest: func(request interaction.Request) {
				field := findSetupField(t, request, "provider")
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
		channel := &setupTestChannel{
			responses: []interaction.Response{{Values: []interaction.Answer{
				{Name: "provider", Value: interaction.StringValue(selected)},
				{Name: "temperature_mode", Value: interaction.StringValue(overrideInherit)},
				{Name: "max_tokens_mode", Value: interaction.StringValue(overrideInherit)},
			}}},
			onRequest: func(interaction.Request) { source.names = remaining },
		}
		op := &setupOperation{scope: scope, providerSources: []model.ProviderSource{source}}
		if _, err := op.Invoke(context.Background(), operation.Request{Interaction: channel}); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("error = %v, want ErrInvalidConfig", err)
		}
		stored, err := loadConfig(scope.Dir())
		if err != nil || stored.Provider != "" {
			t.Fatalf("removed selection was saved: %#v, error = %v", stored, err)
		}
	}
}

func findSetupField(t *testing.T, request interaction.Request, name string) interaction.Field {
	t.Helper()
	for _, field := range request.Fields {
		if field.Name == name {
			return field
		}
	}
	t.Fatalf("field %q not found", name)
	return interaction.Field{}
}
