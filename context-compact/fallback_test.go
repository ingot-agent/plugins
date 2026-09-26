package contextcompact

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	ingotabi "github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/contextwindow"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/tool"
	"github.com/ingot-agent/sdk/usage"
)

type fallbackResolver func(context.Context, model.Request) (model.Request, error)

func (f fallbackResolver) ResolveRequest(ctx context.Context, request model.Request) (model.Request, error) {
	return f(ctx, request)
}

func TestFallbackCountAndDefaultThreshold(t *testing.T) {
	cfg, err := normalizeConfig(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.triggerInputTokens != 800000 || cfg.targetInputTokens != 250000 || cfg.summaryChunkTokens != 128000 || cfg.summaryInputTokens != 256000 {
		t.Fatalf("defaults = %#v", cfg)
	}
	r := &compactor{cfg: cfg}
	request := model.Request{Provider: "p", Model: "m", Messages: []model.Message{{Role: model.RoleUser, Content: content.FromText("hello 中文")}}, Tools: []tool.Definition{{Name: "tool", Description: "description", InputSchema: []byte(`{"type":"object"}`)}}}
	original := cloneRequest(request)
	result, err := r.countRequest(context.Background(), request, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Accuracy != usage.AccuracyEstimate || result.Source != fallbackCountSource || result.InputTokens <= 0 || result.Provider != "p" || result.Model != "m" {
		t.Fatalf("fallback count: %#v", result)
	}
	withoutTools := cloneRequest(request)
	withoutTools.Tools = nil
	small, err := r.countRequest(context.Background(), withoutTools, nil)
	if err != nil || small.InputTokens >= result.InputTokens {
		t.Fatalf("tools not counted: %#v %v", small, err)
	}
	if !reflect.DeepEqual(original, request) {
		t.Fatal("request mutated")
	}
}

func TestFallbackCountsAtCompactionBoundary(t *testing.T) {
	request := firstToolRound()
	r := &compactor{cfg: normalizedConfig{allowedAccuracies: accuracyBit(usage.AccuracyEstimate)}}
	tokens, err := r.countRequest(context.Background(), request, nil)
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	models := &fakeModel{responses: []model.Response{summaryResponse(`{"summary":"investigation remains open","operations":[]}`)}}
	cfg := Config{TriggerInputTokens: tokens.InputTokens, TargetInputTokens: tokens.InputTokens / 2, RecentRounds: 1}
	exports, _, err := New(context.Background(), Dependencies{Model: models, Store: store, State: testStateScope{dir: writeTestConfig(t, cfg)}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := exports.Compactor.Compact(context.Background(), contextwindow.CompactionRequest{SessionID: "s", Invocation: request})
	if err != nil || !result.Changed || len(models.requests) != 1 {
		t.Fatalf("compact result=%+v err=%v calls=%d", result, err, len(models.requests))
	}
	if models.requests[0].Stop != nil {
		t.Fatal("Responses-incompatible Stop on summary")
	}
	restarted, _, err := New(context.Background(), Dependencies{Model: &fakeModel{err: errors.New("unexpected summary")}, Store: store, State: testStateScope{dir: writeTestConfig(t, cfg)}})
	if err != nil {
		t.Fatal(err)
	}
	reused, err := restarted.Compactor.Compact(context.Background(), contextwindow.CompactionRequest{SessionID: "s", Invocation: request})
	if err != nil || !reflect.DeepEqual(result, reused) {
		t.Fatalf("checkpoint reuse: %v", err)
	}
}

func TestFallbackResolverAndAccuracy(t *testing.T) {
	cfg, _ := normalizeConfig(Config{})
	r := &compactor{cfg: cfg}
	request := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: content.FromText("abc")}}}
	if _, err := r.countRequest(context.Background(), request, nil); !errors.Is(err, ErrInvalidCount) {
		t.Fatalf("no selection: %v", err)
	}
	r.resolver = fallbackResolver(func(_ context.Context, got model.Request) (model.Request, error) {
		got.Provider, got.Model = "selected", "model"
		return got, nil
	})
	result, err := r.countRequest(context.Background(), request, nil)
	if err != nil || result.Provider != "selected" || result.Model != "model" {
		t.Fatalf("resolved=%#v err=%v", result, err)
	}
	r.cfg.allowedAccuracies = accuracyBit(usage.AccuracyExact)
	if _, err := r.countRequest(context.Background(), request, nil); !errors.Is(err, ErrUnsupportedAccuracy) {
		t.Fatalf("accuracy: %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.countRequest(canceled, request, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestOptionalDependenciesRejectTypedNil(t *testing.T) {
	for _, tc := range []struct {
		name     string
		counter  ingotabi.Optional[usage.Counter]
		resolver ingotabi.Optional[model.RequestResolver]
	}{
		{name: "counter", counter: ingotabi.Some[usage.Counter]((*canonicalTokenCounter)(nil))},
		{name: "resolver", resolver: ingotabi.Some[model.RequestResolver]((*nilResolver)(nil))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := New(context.Background(), Dependencies{Model: &fakeModel{}, Store: &memoryStore{}, State: testStateScope{dir: t.TempDir()}, Counter: tc.counter, Resolver: tc.resolver})
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatal(err)
			}
		})
	}
}

type nilResolver struct{}

func (*nilResolver) ResolveRequest(context.Context, model.Request) (model.Request, error) {
	panic("should not call")
}

func TestFallbackCounterErrorDoesNotSilentlyDegrade(t *testing.T) {
	cause := errors.New("counter error")
	cfg, _ := normalizeConfig(Config{})
	r := &compactor{cfg: cfg, counter: counterFunc(func(context.Context, usage.CountRequest) (usage.CountResult, error) {
		return usage.CountResult{}, cause
	})}
	_, err := r.countRequest(context.Background(), model.Request{Provider: "p", Model: "m"}, nil)
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "count context input") {
		t.Fatal(err)
	}
}

func TestFallbackCompactionResolvesDefaultModel(t *testing.T) {
	request := firstToolRound()
	request.Provider, request.Model = "", ""
	resolve := fallbackResolver(func(_ context.Context, value model.Request) (model.Request, error) {
		value.Provider, value.Model = "main-provider", "main-model"
		return value, nil
	})
	cfg, err := normalizeConfig(Config{})
	if err != nil {
		t.Fatal(err)
	}
	probe := &compactor{cfg: cfg, resolver: resolve}
	count, err := probe.countRequest(context.Background(), request, nil)
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	models := &fakeModel{responses: []model.Response{summaryResponse(`{"summary":"continue the investigation","operations":[]}`)}}
	settings := Config{TriggerInputTokens: count.InputTokens, TargetInputTokens: count.InputTokens / 2, RecentRounds: 1}
	exports, _, err := New(context.Background(), Dependencies{
		Model: models, Store: store, State: testStateScope{dir: writeTestConfig(t, settings)},
		Resolver: ingotabi.Some[model.RequestResolver](resolve),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := exports.Compactor.Compact(context.Background(), contextwindow.CompactionRequest{SessionID: "s", Invocation: request})
	if err != nil || !result.Changed || len(models.requests) != 1 {
		t.Fatalf("compact=%+v err=%v requests=%d", result, err, len(models.requests))
	}
	if got := models.requests[0]; got.Provider != "main-provider" || got.Model != "main-model" || got.Stop != nil {
		t.Fatalf("summary request = %#v", got)
	}
}

func TestDefaultBudgetCompactsLargeHistory(t *testing.T) {
	// Roughly 800k ASCII input tokens with complete 80k-token rounds. Each
	// summary must cover enough history to reach the 250k target in eight calls.
	const rounds = 10
	messages := []model.Message{{Role: model.RoleSystem, Content: content.FromText("system")}}
	for i := 0; i < rounds; i++ {
		messages = append(messages,
			model.Message{Role: model.RoleUser, Content: content.FromText(strings.Repeat("a", 40000))},
			model.Message{Role: model.RoleAssistant, Content: content.FromText(strings.Repeat("b", 40000))},
		)
	}
	request := model.Request{Provider: "main-provider", Model: "main-model", Messages: messages}
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	calls := 0
	models := protocolModelFunc(func(_ context.Context, summary model.Request) (model.Response, error) {
		calls++
		if summary.Stop != nil {
			t.Fatal("summary Stop must be nil")
		}
		if messageText(summary.Messages[0]) == evidenceSystemPrompt {
			return summaryResponse(`{"summary":"fragment findings"}`), nil
		}
		return summaryResponse(`{"summary":"Earlier rounds had repetitive text.","operations":[]}`), nil
	})
	counter := counterFunc(func(ctx context.Context, input usage.CountRequest) (usage.CountResult, error) {
		if err := ctx.Err(); err != nil {
			return usage.CountResult{}, err
		}
		var tokens int64
		for _, message := range input.Invocation.Messages {
			text, _ := content.TextOnly(message.Content)
			tokens += int64(len(text))
		}
		return counted(tokens), nil
	})
	deps := Dependencies{Model: models, Store: store, State: testStateScope{dir: t.TempDir()}, Counter: ingotabi.Some[usage.Counter](counter)}
	exports, _, err := New(context.Background(), deps)
	if err != nil {
		t.Fatal(err)
	}
	result, err := exports.Compactor.Compact(context.Background(), contextwindow.CompactionRequest{SessionID: "s", Invocation: request})
	if err != nil || !result.Changed || calls == 0 || calls > 8 {
		t.Fatalf("result changed=%v calls=%d err=%v", result.Changed, calls, err)
	}
	countedResult, err := counter.CountInput(context.Background(), usage.CountRequest{Invocation: model.Request{Provider: request.Provider, Model: request.Model, Messages: result.Messages}})
	if err != nil || countedResult.InputTokens > 250000 {
		t.Fatalf("compacted tokens=%d err=%v", countedResult.InputTokens, err)
	}
}
