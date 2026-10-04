package modelruntime_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	ingotabi "github.com/ingot-agent/ingot-abi"
	modelruntime "github.com/ingot-agent/plugins/model-runtime"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/execution"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/observation"
	"github.com/ingot-agent/sdk/pipeline"
	"github.com/ingot-agent/sdk/session"
)

type usageBinder func(execution.Scope) (interaction.Channel, error)

func (f usageBinder) Bind(scope execution.Scope) (interaction.Channel, error) { return f(scope) }

type usageChannel struct {
	interaction.Channel
	set func(context.Context, interaction.State) error
}

func (c usageChannel) Set(ctx context.Context, state interaction.State) error {
	return c.set(ctx, state)
}

func usageResponse(tokens int) model.Response {
	return model.Response{Provider: "p", Model: "m", Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("ok")}, Usage: model.Usage{InputTokens: tokens, TotalTokens: tokens, Reported: true}}
}

func newUsageRuntime(t *testing.T, store *memorySessions, entry model.ProviderEntry, options ...func(*modelruntime.Dependencies)) modelruntime.Exports {
	t.Helper()
	deps := modelruntime.Dependencies{Sessions: store, TokenUsage: store, ProviderSources: []model.ProviderSource{fixedProviderSource(entry)}}
	for _, option := range options {
		option(&deps)
	}
	exports, _, err := modelruntime.New(context.Background(), withState(t, modelruntime.Config{DefaultModel: "m"}, deps))
	if err != nil {
		t.Fatal(err)
	}
	return exports
}

func TestUsageAttributionAndCommittedSessionBoundSnapshots(t *testing.T) {
	store := &memorySessions{totals: map[session.ID]int64{"R": 0, "A": 0, "B": 0, "F": 0, "Q": 0, "C": 0}}
	var mu sync.Mutex
	seen := map[session.ID]int64{}
	binder := usageBinder(func(scope execution.Scope) (interaction.Channel, error) {
		return usageChannel{set: func(ctx context.Context, state interaction.State) error {
			metadata, err := store.Get(ctx, scope.SessionID)
			if err != nil {
				return err
			}
			if state.Name != "model-runtime.session-usage/"+string(scope.SessionID) || len(state.Values) != 2 || state.Values[0].Value.String != string(scope.SessionID) || state.Values[1].Value.Integer != metadata.TotalToken {
				return fmt.Errorf("state preceded persistence or lost scope: %+v", state)
			}
			mu.Lock()
			defer mu.Unlock()
			if metadata.TotalToken < seen[scope.SessionID] {
				t.Error("published usage moved backwards")
			}
			seen[scope.SessionID] = metadata.TotalToken
			return nil
		}}, nil
	})
	entry := model.ProviderEntry{Name: "p", Complete: func(_ context.Context, request model.Request) (model.Response, error) {
		return usageResponse(*request.MaxTokens), nil
	}}
	exports := newUsageRuntime(t, store, entry, func(deps *modelruntime.Dependencies) {
		deps.Interactions = ingotabi.Some[interaction.ExecutionBinder](binder)
	})
	ctx := observation.WithCorrelation(context.Background(), observation.Correlation{SessionID: "unrelated"})
	for _, call := range []struct {
		root, current session.ID
		delta         int
	}{
		{"R", "R", 100}, {"R", "A", 20}, {"R", "B", 30}, {"R", "R", 10}, {"R", "B", 5}, {"F", "F", 7}, {"R", "Q", 11}, {"R", "C", 13},
	} {
		if _, err := exports.Runtime.Complete(ctx, call.root, call.current, model.Request{MaxTokens: &call.delta}); err != nil {
			t.Fatal(err)
		}
	}
	for id, want := range map[session.ID]int64{"R": 189, "A": 20, "B": 35, "F": 7, "Q": 11, "C": 13} {
		metadata, _ := store.Get(ctx, id)
		if metadata.TotalToken != want || seen[id] != want {
			t.Fatalf("%s persisted=%d published=%d want=%d", id, metadata.TotalToken, seen[id], want)
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			delta := 1
			if _, err := exports.Runtime.Complete(ctx, "R", "B", model.Request{MaxTokens: &delta}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if seen["R"] != 213 || seen["B"] != 59 {
		t.Fatalf("concurrent snapshots=%v", seen)
	}
}

func TestUsageOnlyComesFromSuccessfulProviderTerminals(t *testing.T) {
	outerErr := errors.New("outer failed")
	for _, test := range []struct {
		name        string
		interceptor model.Interceptor
		want        int64
		wantErr     error
	}{
		{"short circuit", interceptorFunc(func(context.Context, model.Request, pipeline.Next[model.Request, model.Response]) (model.Response, error) {
			return usageResponse(99), nil
		}), 0, nil},
		{"rewrite usage", interceptorFunc(func(ctx context.Context, req model.Request, next pipeline.Next[model.Request, model.Response]) (model.Response, error) {
			response, err := next(ctx, req)
			response.Usage = usageResponse(99).Usage
			return response, err
		}), 7, nil},
		{"fail after provider", interceptorFunc(func(ctx context.Context, req model.Request, next pipeline.Next[model.Request, model.Response]) (model.Response, error) {
			_, err := next(ctx, req)
			if err != nil {
				return model.Response{}, err
			}
			return model.Response{}, outerErr
		}), 7, outerErr},
		{"two provider calls", interceptorFunc(func(ctx context.Context, req model.Request, next pipeline.Next[model.Request, model.Response]) (model.Response, error) {
			if _, err := next(ctx, req); err != nil {
				return model.Response{}, err
			}
			return next(ctx, req)
		}), 14, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &memorySessions{totals: map[session.ID]int64{"s": 0}}
			exports := newUsageRuntime(t, store, model.ProviderEntry{Name: "p", Complete: func(context.Context, model.Request) (model.Response, error) { return usageResponse(7), nil }}, func(deps *modelruntime.Dependencies) { deps.Interceptors = []model.Interceptor{test.interceptor} })
			_, err := exports.Runtime.Complete(context.Background(), "s", "s", model.Request{})
			if !errors.Is(err, test.wantErr) || store.totals["s"] != test.want {
				t.Fatalf("total=%d err=%v", store.totals["s"], err)
			}
		})
	}
}

func TestUsageValidationAndFailureBoundaries(t *testing.T) {
	providerErr := errors.New("provider failed")
	for _, test := range []struct {
		name        string
		response    model.Response
		providerErr error
		want        int64
		settlements int
		wantErr     error
	}{
		{"unreported", model.Response{Message: usageResponse(0).Message}, nil, 0, 0, nil},
		{"reported zero", usageResponse(0), nil, 0, 1, nil},
		{"provider error is not authoritative", usageResponse(7), providerErr, 0, 0, providerErr},
		{"invalid message still costs tokens", model.Response{Usage: usageResponse(7).Usage}, nil, 7, 1, modelruntime.ErrInvalidResponse},
		{"inconsistent usage", model.Response{Message: usageResponse(0).Message, Usage: model.Usage{Reported: true, InputTokens: 2, TotalTokens: 3}}, nil, 0, 0, modelruntime.ErrInvalidResponse},
		{"negative usage", model.Response{Message: usageResponse(0).Message, Usage: model.Usage{Reported: true, InputTokens: -1, TotalTokens: -1}}, nil, 0, 0, modelruntime.ErrInvalidResponse},
		{"counts without presence", model.Response{Message: usageResponse(0).Message, Usage: model.Usage{InputTokens: 7, TotalTokens: 7}}, nil, 0, 0, modelruntime.ErrInvalidResponse},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &memorySessions{totals: map[session.ID]int64{"s": 0}}
			exports := newUsageRuntime(t, store, model.ProviderEntry{Name: "p", Complete: func(context.Context, model.Request) (model.Response, error) { return test.response, test.providerErr }})
			_, err := exports.Runtime.Complete(context.Background(), "s", "s", model.Request{})
			if !errors.Is(err, test.wantErr) || store.totals["s"] != test.want || store.settlements != test.settlements {
				t.Fatalf("total=%d settlements=%d err=%v", store.totals["s"], store.settlements, err)
			}
		})
	}
	for _, setFails := range []bool{false, true} {
		t.Run(fmt.Sprintf("set fails=%v", setFails), func(t *testing.T) {
			persistErr := errors.New("storage failed")
			store := &memorySessions{totals: map[session.ID]int64{"s": 0}}
			if !setFails {
				store.err = persistErr
			}
			calls, sets := 0, 0
			exports := newUsageRuntime(t, store, model.ProviderEntry{Name: "p", Complete: func(context.Context, model.Request) (model.Response, error) { calls++; return usageResponse(7), nil }}, func(deps *modelruntime.Dependencies) {
				deps.Interactions = ingotabi.Some[interaction.ExecutionBinder](usageBinder(func(execution.Scope) (interaction.Channel, error) {
					return usageChannel{set: func(context.Context, interaction.State) error { sets++; return errors.New("offline UI") }}, nil
				}))
			})
			_, err := exports.Runtime.Complete(context.Background(), "s", "s", model.Request{})
			if calls != 1 {
				t.Fatalf("provider was retried: %d", calls)
			}
			if setFails {
				if err != nil || store.totals["s"] != 7 || sets != 1 {
					t.Fatalf("err=%v sets=%d", err, sets)
				}
			} else if !errors.Is(err, modelruntime.ErrUsagePersistence) || !errors.Is(err, persistErr) || sets != 0 {
				t.Fatalf("err=%v sets=%d", err, sets)
			}
		})
	}
}

func TestUsageStreamAndCanceledSettlement(t *testing.T) {
	for _, incomplete := range []bool{false, true} {
		t.Run(fmt.Sprintf("incomplete=%v", incomplete), func(t *testing.T) {
			store := &memorySessions{totals: map[session.ID]int64{"r": 0, "s": 0}}
			entry := model.ProviderEntry{Name: "p", Complete: func(context.Context, model.Request) (model.Response, error) { return usageResponse(7), nil }, Stream: func(_ context.Context, _ model.Request, handler model.StreamHandler) (model.Response, error) {
				for _, event := range []model.StreamEvent{{Kind: model.StreamPartStart, PartKind: content.KindText}, {Kind: model.StreamPartDelta, TextDelta: "ok"}, {Kind: model.StreamPartEnd}} {
					if incomplete && event.Kind == model.StreamPartEnd {
						break
					}
					if err := handler(event); err != nil {
						return model.Response{}, err
					}
				}
				return usageResponse(7), nil
			}}
			exports := newUsageRuntime(t, store, entry)
			_, err := exports.Streaming.Stream(context.Background(), "r", "s", model.Request{}, func(model.StreamEvent) error { return nil })
			if (incomplete && !errors.Is(err, modelruntime.ErrInvalidResponse)) || (!incomplete && err != nil) || store.totals["r"] != 7 || store.totals["s"] != 7 || store.settlements != 1 {
				t.Fatalf("totals=%v err=%v", store.totals, err)
			}
			consumerErr := errors.New("consumer stopped")
			_, err = exports.Streaming.Stream(context.Background(), "r", "s", model.Request{}, func(model.StreamEvent) error { return consumerErr })
			if !errors.Is(err, consumerErr) || store.settlements != 1 {
				t.Fatalf("interrupted stream settled: %v", err)
			}
		})
	}
	store := &memorySessions{totals: map[session.ID]int64{"s": 0}}
	ctx, cancel := context.WithCancel(context.Background())
	exports := newUsageRuntime(t, store, model.ProviderEntry{Name: "p", Complete: func(context.Context, model.Request) (model.Response, error) { cancel(); return usageResponse(7), nil }})
	_, _ = exports.Runtime.Complete(ctx, "s", "s", model.Request{})
	if store.totals["s"] != 7 {
		t.Fatal("caller cancellation erased authoritative provider usage")
	}
	if _, err := exports.Runtime.Complete(context.Background(), "missing", "s", model.Request{}); !errors.Is(err, session.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := exports.Runtime.Complete(context.Background(), "", "s", model.Request{}); err == nil {
		t.Fatal("empty root accepted")
	}
}
