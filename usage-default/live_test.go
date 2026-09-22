package usagedefault

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/operation"
	"github.com/ingot-agent/sdk/usage"
)

type liveConfigChannel struct{ config Config }

func (c *liveConfigChannel) Request(context.Context, interaction.Request) (interaction.Response, error) {
	return interaction.Response{Values: []interaction.Answer{
		{Name: "routes", Value: usageRoutesValue(c.config.Routes)},
		{Name: "cache_entries", Value: interaction.IntegerValue(int64(c.config.CacheEntries))},
	}}, nil
}

func (*liveConfigChannel) Emit(context.Context, interaction.Event) error { return nil }
func (*liveConfigChannel) Set(context.Context, interaction.State) error  { return nil }
func (*liveConfigChannel) Clear(context.Context, string) error           { return nil }

func TestSetupUpdatesRunningCounterRoutesAndCache(t *testing.T) {
	ctx := context.Background()
	original := validConfig()
	original.CacheEntries = 3
	scope := testStateScope{dir: writeTestConfig(t, original)}
	exports, cleanup, err := New(ctx, Dependencies{State: scope, Resolver: resolverFunc(passthroughResolver)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cleanup(ctx) })
	counter := exports.Counter.(*counter)
	request := usage.CountRequest{Invocation: validRequest("live configuration probe")}
	baseline, err := exports.Counter.CountInput(ctx, request)
	if err != nil || baseline.InputTokens <= 0 || baseline.Source != unicodeEstimateSource {
		t.Fatalf("baseline = %#v, error = %v", baseline, err)
	}

	assertCache := func(want int) {
		t.Helper()
		counter.mu.Lock()
		defer counter.mu.Unlock()
		if len(counter.cache) != want || counter.recent.Len() != want {
			t.Fatalf("cache entries = %d, LRU entries = %d, want %d", len(counter.cache), counter.recent.Len(), want)
		}
	}
	apply := func(config Config) error {
		t.Helper()
		result, err := exports.Operations[0].Invoke(ctx, operation.Request{Interaction: &liveConfigChannel{config: config}})
		if err != nil {
			return err
		}
		var output map[string]any
		if err := json.Unmarshal(result.Output, &output); err != nil || output["restart_required"] != false {
			t.Fatalf("output = %s, error = %v", result.Output, err)
		}
		return nil
	}
	assertCache(1)

	updated := Config{Routes: []Route{{Provider: "next", ModelPattern: "model", Profile: unicodeEstimateSource}}, CacheEntries: 1}
	if err := apply(updated); err != nil {
		t.Fatal(err)
	}
	assertCache(0)
	if _, err := exports.Counter.CountInput(ctx, request); !errors.Is(err, ErrUnsupportedModel) {
		t.Fatalf("old provider error = %v, want ErrUnsupportedModel", err)
	}
	next := request
	next.Invocation.Provider = "next"
	result, err := exports.Counter.CountInput(ctx, next)
	if err != nil || result.Provider != "next" || result.InputTokens != baseline.InputTokens {
		t.Fatalf("new route = %#v, error = %v", result, err)
	}
	for _, text := range []string{"second input", "third input"} {
		invocation := validRequest(text)
		invocation.Provider = "next"
		if _, err := exports.Counter.CountInput(ctx, usage.CountRequest{Invocation: invocation}); err != nil {
			t.Fatal(err)
		}
		assertCache(1)
	}
	t.Log("live route switch, cache invalidation, and reduced capacity passed")

	invalid := Config{Routes: []Route{{Provider: "invalid", ModelPattern: "[", Profile: unicodeEstimateSource}}, CacheEntries: 1}
	if err := apply(invalid); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("invalid configuration error = %v, want ErrInvalidConfig", err)
	}
	if _, err := exports.Counter.CountInput(ctx, next); err != nil {
		t.Fatalf("invalid update changed the running route: %v", err)
	}
	stored, err := loadConfig(scope.Dir())
	if err != nil || len(stored.Routes) != 1 || stored.Routes[0].Provider != "next" {
		t.Fatalf("invalid update changed persisted routes: %#v, error = %v", stored, err)
	}

	if err := apply(original); err != nil {
		t.Fatal(err)
	}
	assertCache(0)
	restored, err := exports.Counter.CountInput(ctx, request)
	if err != nil || restored != baseline {
		t.Fatalf("restored count = %#v, want %#v, error = %v", restored, baseline, err)
	}
	if _, err := exports.Counter.CountInput(ctx, next); !errors.Is(err, ErrUnsupportedModel) {
		t.Fatalf("removed provider error = %v, want ErrUnsupportedModel", err)
	}
	for _, text := range []string{"second input", "third input"} {
		if _, err := exports.Counter.CountInput(ctx, usage.CountRequest{Invocation: validRequest(text)}); err != nil {
			t.Fatal(err)
		}
	}
	assertCache(3)
	t.Log("invalid update preserved state; original routes and capacity restored without rebuilding the counter")
}
