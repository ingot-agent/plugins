package agentdefault

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/tool"
)

type requestResolverFunc func(context.Context, model.Request) (model.Request, error)

func (f requestResolverFunc) ResolveRequest(ctx context.Context, request model.Request) (model.Request, error) {
	return f(ctx, request)
}

func TestTurnRetainsResolvedModelDefaults(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, initialEffort := range []model.ReasoningEffort{"", model.ReasoningEffortHigh} {
			t.Run(fmtTurnCase(streaming, initialEffort), func(t *testing.T) {
				ctx := context.Background()
				current := model.Request{Provider: "first", Model: "old", ReasoningEffort: initialEffort}
				resolutions := 0
				resolver := requestResolverFunc(func(_ context.Context, request model.Request) (model.Request, error) {
					resolutions++
					if request.Provider != "" || request.Model != "" || request.ReasoningEffort != "" {
						t.Fatalf("Agent supplied its own model defaults: %#v", request)
					}
					return current, nil
				})
				models := &sequenceModel{responses: []model.Response{
					{Message: model.Message{Role: model.RoleAssistant, ToolCalls: []tool.Call{{ID: "c1", Name: "echo", Arguments: json.RawMessage(`{}`)}}}},
					{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("done")}},
					{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("next")}},
				}}
				tools := &selectionChangingTools{change: func() {
					current = model.Request{Provider: "second", Model: "new", ReasoningEffort: model.ReasoningEffortLow}
				}}
				deps := withState(t, Config{}, Dependencies{
					Model: models, Resolver: ingotabi.Some[model.RequestResolver](resolver), Tools: tools,
					Store: &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}, Assets: newMemoryAssets(), Prompt: passthroughPrompt{},
				})
				if streaming {
					deps.Streaming = ingotabi.Some[model.StreamingRuntime](modelStreamFunc(func(ctx context.Context, request model.Request, _ model.StreamHandler) (model.Response, error) {
						return models.Complete(ctx, "s", "s", request)
					}))
				}
				// Retired overrides must not win over model-runtime, even in old files.
				if err := os.WriteFile(filepath.Join(deps.State.Dir(), configFileName), []byte("provider = 'legacy'\nmodel = 'legacy-model'\nreasoning_effort = 'high'\nprovider_default_reasoning = true\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				exports, _, err := New(ctx, deps)
				if err != nil {
					t.Fatal(err)
				}
				for i := 0; i < 2; i++ {
					turn := agent.Turn{SessionID: "s", Input: "hello"}
					if streaming {
						_, err = exports.Streaming.Stream(ctx, turn, func(agent.StreamEvent) error { return nil })
					} else {
						_, err = exports.Runtime.Run(ctx, turn)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				if len(models.requests) != 3 || resolutions != 2 {
					t.Fatalf("model calls = %d, resolutions = %d", len(models.requests), resolutions)
				}
				wantEffort := initialEffort
				if wantEffort == "" {
					wantEffort = model.ReasoningEffortProviderDefault
				}
				for _, request := range models.requests[:2] {
					if request.Provider != "first" || request.Model != "old" || request.ReasoningEffort != wantEffort {
						t.Fatalf("selection changed within a turn: %#v", request)
					}
				}
				if request := models.requests[2]; request.Provider != "second" || request.Model != "new" || request.ReasoningEffort != model.ReasoningEffortLow {
					t.Fatalf("next turn did not use new defaults: %#v", request)
				}
			})
		}
	}
}

func fmtTurnCase(streaming bool, effort model.ReasoningEffort) string {
	mode := "complete/"
	if streaming {
		mode = "stream/"
	}
	return mode + string(effort)
}

type selectionChangingTools struct {
	fakeTools
	change func()
}

func (t *selectionChangingTools) Call(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
	t.change()
	return t.fakeTools.Call(ctx, invocation)
}

func TestLegacyModelConfigIsIgnoredAndDroppedOnSave(t *testing.T) {
	scope := t.TempDir()
	path := filepath.Join(scope, configFileName)
	if err := os.WriteFile(path, []byte("provider = 'p'\nmodel = 'm'\nreasoning_effort = 'old-value'\nprovider_default_reasoning = true\ntemperature = 0.25\nmax_tokens = 512\nmax_rounds = 4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadConfig(scope)
	if err != nil || loaded.MaxRounds != 4 || loaded.Temperature == nil || *loaded.Temperature != 0.25 || loaded.MaxTokens == nil || *loaded.MaxTokens != 512 {
		t.Fatalf("legacy config = %#v, err = %v", loaded, err)
	}
	if err := saveConfig(scope, loaded); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"provider", "model", "reasoning_effort"} {
		if strings.Contains(string(raw), field) {
			t.Fatalf("retired model setting persisted: %s", raw)
		}
	}
	if err := os.WriteFile(path, []byte("unknown_setting = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(scope); err == nil {
		t.Fatal("unknown fields must still be rejected")
	}
}

func TestModelResolverFailureStopsTurn(t *testing.T) {
	want := errors.New("no configured model")
	models := &sequenceModel{}
	exports, _, err := New(context.Background(), withState(t, Config{}, Dependencies{
		Model: models, Tools: &fakeTools{}, Store: &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}, Assets: newMemoryAssets(), Prompt: passthroughPrompt{},
		Resolver: ingotabi.Some[model.RequestResolver](requestResolverFunc(func(context.Context, model.Request) (model.Request, error) { return model.Request{}, want })),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exports.Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "hello"}); !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
	if len(models.requests) != 0 {
		t.Fatal("model was invoked without a resolved selection")
	}
}
