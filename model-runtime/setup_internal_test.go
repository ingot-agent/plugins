package modelruntime

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/operation"
)

type setupTestScope struct{ dir string }

func (s setupTestScope) Dir() string { return s.dir }

type setupTestProvider struct{}

func (setupTestProvider) Complete(context.Context, model.Request) (model.Response, error) {
	return model.Response{}, nil
}

type setupTestChannel struct {
	request interaction.Request
	respond func(interaction.Request) (interaction.Response, error)
}

func (c *setupTestChannel) Request(_ context.Context, request interaction.Request) (interaction.Response, error) {
	c.request = request
	return c.respond(request)
}

func (*setupTestChannel) Emit(context.Context, interaction.Event) error { return nil }
func (*setupTestChannel) Set(context.Context, interaction.State) error  { return nil }
func (*setupTestChannel) Clear(context.Context, string) error           { return nil }

func TestSetupUsesClosedProviderOptionsAndRejectsUnknownProvider(t *testing.T) {
	scope := setupTestScope{dir: filepath.Join(t.TempDir(), "state")}
	exports, _, err := New(context.Background(), Dependencies{
		State: scope,
		Providers: []ingotabi.Named[model.Provider]{
			{Name: "first", Value: setupTestProvider{}},
			{Name: "second", Value: setupTestProvider{}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	channel := &setupTestChannel{respond: func(request interaction.Request) (interaction.Response, error) {
		provider := findSetupField(t, request, "default_provider")
		if provider.Kind != interaction.FieldChoice || provider.Default != nil || len(provider.Options) != 2 || provider.Options[0].Value != "first" || provider.Options[1].Value != "second" {
			t.Fatalf("provider field = %#v", provider)
		}
		return interaction.Response{Values: []interaction.Answer{
			{Name: "default_provider", Value: interaction.StringValue("missing")},
			{Name: "default_model", Value: interaction.StringValue("model")},
		}}, nil
	}}
	_, err = exports.Operations[0].Invoke(context.Background(), operation.Request{Interaction: channel})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error = %v, want ErrInvalidConfig", err)
	}
	stored, err := loadConfig(scope.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if stored != (Config{}) {
		t.Fatalf("stored = %#v, want empty", stored)
	}
}

func TestSetupRejectsEmptyProviderWithMultipleOptions(t *testing.T) {
	scope := setupTestScope{dir: filepath.Join(t.TempDir(), "state")}
	exports, _, err := New(context.Background(), Dependencies{
		State: scope,
		Providers: []ingotabi.Named[model.Provider]{
			{Name: "first", Value: setupTestProvider{}},
			{Name: "second", Value: setupTestProvider{}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	channel := &setupTestChannel{respond: func(interaction.Request) (interaction.Response, error) {
		return interaction.Response{Values: []interaction.Answer{{Name: "default_provider", Value: interaction.StringValue("")}}}, nil
	}}
	_, err = exports.Operations[0].Invoke(context.Background(), operation.Request{Interaction: channel})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error = %v, want ErrInvalidConfig", err)
	}
	stored, err := loadConfig(scope.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if stored != (Config{}) {
		t.Fatalf("stored = %#v, want empty", stored)
	}
}

func TestSetupSingleProviderAutomaticIsNotAChange(t *testing.T) {
	scope := setupTestScope{dir: filepath.Join(t.TempDir(), "state")}
	exports, _, err := New(context.Background(), Dependencies{
		State:     scope,
		Providers: []ingotabi.Named[model.Provider]{{Name: "only", Value: setupTestProvider{}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	channel := &setupTestChannel{respond: defaultsResponse}
	result, err := exports.Operations[0].Invoke(context.Background(), operation.Request{Interaction: channel})
	if err != nil {
		t.Fatal(err)
	}
	provider := findSetupField(t, channel.request, "default_provider")
	if len(provider.Options) != 2 || provider.Options[0].Value != "" || provider.Options[1].Value != "only" {
		t.Fatalf("provider options = %#v", provider.Options)
	}
	var output struct {
		RestartRequired bool `json:"restart_required"`
	}
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	if output.RestartRequired {
		t.Fatal("automatic single-provider selection should match the active runtime")
	}
}

func defaultsResponse(request interaction.Request) (interaction.Response, error) {
	answers := make([]interaction.Answer, 0, len(request.Fields))
	for _, field := range request.Fields {
		if field.Default != nil {
			answers = append(answers, interaction.Answer{Name: field.Name, Value: *field.Default})
		}
	}
	return interaction.Response{Values: answers}, nil
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
