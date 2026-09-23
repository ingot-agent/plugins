package usagedefault

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/operation"
)

type setupChannel struct {
	request      interaction.Request
	onRequest    func(interaction.Request)
	cacheEntries *int64
}

func (c *setupChannel) Request(_ context.Context, request interaction.Request) (interaction.Response, error) {
	c.request = request
	if c.onRequest != nil {
		c.onRequest(request)
	}
	answers := make([]interaction.Answer, 0, len(request.Fields))
	for _, field := range request.Fields {
		if field.Default != nil {
			value := *field.Default
			if field.Name == "cache_entries" && c.cacheEntries != nil {
				value = interaction.IntegerValue(*c.cacheEntries)
			}
			answers = append(answers, interaction.Answer{Name: field.Name, Value: value})
		}
	}
	return interaction.Response{Values: answers}, nil
}

func (*setupChannel) Emit(context.Context, interaction.Event) error { return nil }
func (*setupChannel) Set(context.Context, interaction.State) error  { return nil }
func (*setupChannel) Clear(context.Context, string) error           { return nil }

func TestSetupOnlyConfiguresCacheAndDropsLegacyRoutes(t *testing.T) {
	current := Config{Routes: []Route{{Provider: "old", ModelPattern: ".*", Profile: deepSeekV4Source}}}
	scope := testStateScope{dir: writeTestConfig(t, current)}
	counter := newCounter(resolverFunc(passthroughResolver), unicodeEstimateProfile{}, defaultCacheEntries)
	channel := &setupChannel{}
	op := &setupOperation{scope: scope, counter: counter}
	result, err := op.Invoke(context.Background(), operation.Request{Interaction: channel})
	if err != nil {
		t.Fatal(err)
	}
	if len(channel.request.Fields) != 1 || channel.request.Fields[0].Name != "cache_entries" {
		t.Fatalf("setup fields = %#v", channel.request.Fields)
	}
	stored, err := loadConfig(scope.Dir())
	if err != nil || len(stored.Routes) != 0 || stored.CacheEntries != 0 {
		t.Fatalf("stored config = %+v, error = %v", stored, err)
	}
	if counter.config.Load().capacity != defaultCacheEntries {
		t.Fatalf("running capacity = %d", counter.config.Load().capacity)
	}
	var output struct {
		RestartRequired bool `json:"restart_required"`
	}
	if err := json.Unmarshal(result.Output, &output); err != nil || output.RestartRequired {
		t.Fatalf("output = %s, error = %v", result.Output, err)
	}
}

func TestSetupCacheChangeAppliesWithoutRestart(t *testing.T) {
	current := Config{CacheEntries: 12}
	scope := testStateScope{dir: writeTestConfig(t, current)}
	counter := newCounter(resolverFunc(passthroughResolver), unicodeEstimateProfile{}, 12)
	changed := int64(24)
	channel := &setupChannel{cacheEntries: &changed}
	op := &setupOperation{scope: scope, counter: counter}
	result, err := op.Invoke(context.Background(), operation.Request{Interaction: channel})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := loadConfig(scope.Dir())
	if err != nil || stored.CacheEntries != 24 || counter.config.Load().capacity != 24 {
		t.Fatalf("stored config = %+v, running capacity = %d, error = %v", stored, counter.config.Load().capacity, err)
	}
	var output struct {
		RestartRequired bool `json:"restart_required"`
	}
	if err := json.Unmarshal(result.Output, &output); err != nil || output.RestartRequired {
		t.Fatalf("output = %s, error = %v", result.Output, err)
	}
}

func TestSetupDetectsConcurrentConfigurationChange(t *testing.T) {
	scope := testStateScope{dir: writeTestConfig(t, validConfig())}
	channel := &setupChannel{onRequest: func(interaction.Request) {
		if err := saveConfig(scope.Dir(), Config{CacheEntries: 3}); err != nil {
			t.Fatal(err)
		}
	}}
	op := &setupOperation{scope: scope}
	if _, err := op.Invoke(context.Background(), operation.Request{Interaction: channel}); !errors.Is(err, ErrConfigConflict) {
		t.Fatalf("error = %v, want ErrConfigConflict", err)
	}
}
