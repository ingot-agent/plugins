package usagedefault

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
	onRequest func(interaction.Request)
}

func (c *setupChannel) Request(_ context.Context, request interaction.Request) (interaction.Response, error) {
	c.request = request
	if c.onRequest != nil {
		c.onRequest(request)
	}
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
	op := &setupOperation{scope: scope, providerSources: []model.ProviderSource{&setupProviderSource{names: []string{"first", "second"}}}, counter: setupCounter(t, active)}
	if _, err := op.Invoke(context.Background(), operation.Request{Interaction: channel}); err != nil {
		t.Fatal(err)
	}
	running := op.counter.config.Load()
	if running.capacity != defaultCacheEntries || len(running.routes) != len(current.Routes) || running.routes[0].provider != current.Routes[0].Provider {
		t.Fatalf("running config = %#v", running)
	}
	routes := findSetupField(t, channel.request.Fields, "routes")
	provider := findSetupField(t, routes.Element.Fields, "provider")
	if provider.Kind != interaction.FieldString || len(provider.Options) != 2 || provider.Options[0].Value != "first" || provider.Options[1].Value != "second" {
		t.Fatalf("provider field = %#v", provider)
	}
}

func TestSetupRefreshesProviderSuggestions(t *testing.T) {
	current := validConfig()
	scope := testStateScope{dir: writeTestConfig(t, current)}
	source := &setupProviderSource{}
	op := &setupOperation{scope: scope, providerSources: []model.ProviderSource{source}, counter: setupCounter(t, cloneConfig(current))}
	for _, names := range [][]string{nil, {"new", "second"}, {"renamed"}} {
		source.names = names
		channel := &setupChannel{}
		if _, err := op.Invoke(context.Background(), operation.Request{Interaction: channel}); err != nil {
			t.Fatal(err)
		}
		routes := findSetupField(t, channel.request.Fields, "routes")
		provider := findSetupField(t, routes.Element.Fields, "provider")
		if provider.Kind != interaction.FieldString || len(provider.Options) != len(names) {
			t.Fatalf("provider field = %#v", provider)
		}
		for i, name := range names {
			if provider.Options[i].Value != name {
				t.Fatalf("provider options = %#v", provider.Options)
			}
		}
		stored, err := loadConfig(scope.Dir())
		if err != nil || stored.Routes[0].Provider != current.Routes[0].Provider {
			t.Fatalf("future route was not preserved: %#v, error = %v", stored, err)
		}
	}
}

func TestSetupRereadsProviderSourcesBeforeSaving(t *testing.T) {
	scope := testStateScope{dir: writeTestConfig(t, validConfig())}
	source := &setupProviderSource{names: []string{"available"}}
	sourceErr := errors.New("source unavailable")
	channel := &setupChannel{onRequest: func(interaction.Request) { source.err = sourceErr }}
	op := &setupOperation{scope: scope, providerSources: []model.ProviderSource{source}, counter: setupCounter(t, validConfig())}
	if _, err := op.Invoke(context.Background(), operation.Request{Interaction: channel}); !errors.Is(err, sourceErr) {
		t.Fatalf("error = %v, want source error", err)
	}
}

func setupCounter(t *testing.T, configuration Config) *counter {
	t.Helper()
	profiles, err := builtInProfiles()
	if err != nil {
		t.Fatal(err)
	}
	routes, err := compileRoutes(configuration.Routes, profiles)
	if err != nil {
		t.Fatal(err)
	}
	capacity := configuration.CacheEntries
	if capacity == 0 {
		capacity = defaultCacheEntries
	}
	return newCounter(nil, routes, capacity)
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
