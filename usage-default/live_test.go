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

type liveConfigChannel struct{ cacheEntries int64 }

func (c *liveConfigChannel) Request(context.Context, interaction.Request) (interaction.Response, error) {
	return interaction.Response{Values: []interaction.Answer{
		{Name: "cache_entries", Value: interaction.IntegerValue(c.cacheEntries)},
	}}, nil
}

func (*liveConfigChannel) Emit(context.Context, interaction.Event) error { return nil }
func (*liveConfigChannel) Set(context.Context, interaction.State) error  { return nil }
func (*liveConfigChannel) Clear(context.Context, string) error           { return nil }

func TestSetupUpdatesRunningCounterCapacity(t *testing.T) {
	ctx := context.Background()
	scope := testStateScope{dir: writeTestConfig(t, Config{CacheEntries: 3})}
	exports, cleanup, err := New(ctx, Dependencies{State: scope, Resolver: resolverFunc(passthroughResolver)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cleanup(ctx) })
	counter := exports.Counter.(*counter)
	request := usage.CountRequest{Invocation: validRequest("live configuration probe")}
	baseline, err := counter.CountInput(ctx, request)
	if err != nil || baseline.Source != unicodeEstimateSource {
		t.Fatalf("baseline = %+v, error = %v", baseline, err)
	}
	assertCache := func(want int) {
		t.Helper()
		counter.mu.Lock()
		defer counter.mu.Unlock()
		if len(counter.cache) != want || counter.recent.Len() != want {
			t.Fatalf("cache entries = %d, LRU entries = %d, want %d", len(counter.cache), counter.recent.Len(), want)
		}
	}
	apply := func(capacity int64) error {
		t.Helper()
		result, err := exports.Operations[0].Invoke(ctx, operation.Request{Interaction: &liveConfigChannel{cacheEntries: capacity}})
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
	if err := apply(1); err != nil {
		t.Fatal(err)
	}
	assertCache(0)
	if counter.config.Load().capacity != 1 {
		t.Fatalf("running capacity = %d", counter.config.Load().capacity)
	}
	for _, invocation := range []usage.CountRequest{request, {Invocation: validRequest("another input")}} {
		result, err := counter.CountInput(ctx, invocation)
		if err != nil || result.Source != unicodeEstimateSource {
			t.Fatalf("result = %+v, error = %v", result, err)
		}
		assertCache(1)
	}
	if err := apply(-1); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("invalid configuration error = %v", err)
	}
	if counter.config.Load().capacity != 1 {
		t.Fatalf("invalid update changed running capacity to %d", counter.config.Load().capacity)
	}
	stored, err := loadConfig(scope.Dir())
	if err != nil || stored.CacheEntries != 1 {
		t.Fatalf("persisted config = %+v, error = %v", stored, err)
	}
	if err := apply(3); err != nil {
		t.Fatal(err)
	}
	assertCache(0)
	if counter.config.Load().capacity != 3 {
		t.Fatalf("restored capacity = %d", counter.config.Load().capacity)
	}
}
