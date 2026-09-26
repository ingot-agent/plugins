package contextcompact

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	ingotabi "github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/contextwindow"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/tool"
	"github.com/ingot-agent/sdk/usage"
)

type counterFunc func(context.Context, usage.CountRequest) (usage.CountResult, error)

func (f counterFunc) CountInput(ctx context.Context, request usage.CountRequest) (usage.CountResult, error) {
	return f(ctx, request)
}

func counted(tokens int64) usage.CountResult {
	return usage.CountResult{InputTokens: tokens, Accuracy: usage.AccuracyEstimate, Source: "test-token-profile-v1", Provider: "main-provider", Model: "main-model"}
}

func firstToolRound() model.Request {
	return model.Request{Provider: "main-provider", Model: "main-model", Messages: []model.Message{
		{Role: model.RoleSystem, Content: textContent("system")},
		{Role: model.RoleUser, Content: textContent("Find the failure and keep the exact file path.")},
		{Role: model.RoleAssistant, ToolCalls: []tool.Call{{ID: "c1", Name: "read_log", Arguments: json.RawMessage("{}")}}},
		{Role: model.RoleTool, ToolCallID: "c1", Content: textContent(strings.Repeat("log data\n", 1400))},
	}}
}

func newTokenTestCompactor(t *testing.T, cfg Config, counter usage.Counter, models model.Runtime, store *memoryStore) contextwindow.Compactor {
	t.Helper()
	exports, _, err := New(context.Background(), withState(t, cfg, Dependencies{Model: models, Counter: ingotabi.Some[usage.Counter](counter), Store: store}))
	if err != nil {
		t.Fatal(err)
	}
	return exports.Compactor
}

func TestTokenBudgetUsesCounterInsteadOfCanonicalBytes(t *testing.T) {
	request := firstToolRound()
	original := cloneRequest(request)
	models := &fakeModel{}
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	counter := counterFunc(func(_ context.Context, got usage.CountRequest) (usage.CountResult, error) {
		if !reflect.DeepEqual(got.Invocation, original) {
			t.Fatal("counter did not receive the full invocation")
		}
		got.Invocation.Messages[0].Content = textContent("counter mutation")
		return counted(10), nil
	})
	compactor := newTokenTestCompactor(t, Config{TriggerInputTokens: 20, TargetInputTokens: 15}, counter, models, store)
	result, err := compactor.Compact(context.Background(), contextwindow.CompactionRequest{SessionID: "s", Invocation: request})
	if err != nil || result.Changed || !reflect.DeepEqual(result.Messages, original.Messages) || len(models.requests) != 0 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, len(models.requests))
	}
	if !reflect.DeepEqual(request, original) {
		t.Fatal("counter mutated caller invocation")
	}
}

func TestFirstHugeRoundCompactsThroughSoftRecentAndRestarts(t *testing.T) {
	request := firstToolRound()
	request.Messages = append(request.Messages, model.Message{Role: model.RoleUser, Content: textContent("unfinished next turn")})
	original := cloneRequest(request)
	models := &fakeModel{responses: []model.Response{summaryResponse(`{"summary":"Failure is in /tmp/build.log; investigation remains open.","operations":[]}`)}}
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	cfg := Config{TriggerInputTokens: 3000, TargetInputTokens: 1400, RecentRounds: 4}
	compactor := newTokenTestCompactor(t, cfg, &canonicalTokenCounter{}, models, store)
	result, err := compactor.Compact(context.Background(), contextwindow.CompactionRequest{SessionID: "s", Invocation: request})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || len(models.requests) != 1 || len(result.Messages) != 3 {
		t.Fatalf("result=%+v calls=%d", result, len(models.requests))
	}
	if !reflect.DeepEqual(result.Messages[2], request.Messages[4]) {
		t.Fatal("open user suffix changed")
	}
	if !reflect.DeepEqual(request, original) {
		t.Fatal("raw history changed")
	}
	checkpoint, err := decodeCheckpoint(store.entries["s"][0].Payload)
	if err != nil || checkpoint.CoveredMessages != 3 {
		t.Fatalf("checkpoint=%+v err=%v", checkpoint, err)
	}
	if store.entries["s"][0].Version != 2 {
		t.Fatal("expected v2 checkpoint")
	}
	restartedModels := &fakeModel{err: errors.New("must not summarize the same first round again")}
	restarted := newTokenTestCompactor(t, cfg, &canonicalTokenCounter{}, restartedModels, store)
	reused, err := restarted.Compact(context.Background(), contextwindow.CompactionRequest{SessionID: "s", Invocation: request})
	if err != nil || !reflect.DeepEqual(reused, result) {
		t.Fatalf("reused=%+v err=%v", reused, err)
	}
}

func TestCounterControlsTriggerAndExactTargetIncludingTools(t *testing.T) {
	for _, before := range []int64{99, 100} {
		t.Run(string(rune(before)), func(t *testing.T) {
			request := model.Request{Provider: "main-provider", Model: "main-model", Messages: []model.Message{
				{Role: model.RoleUser, Content: textContent("u")}, {Role: model.RoleAssistant, Content: textContent("a")},
			}, Tools: []tool.Definition{{Name: "large_schema", Description: "expensive schema", InputSchema: json.RawMessage(`{"type":"object"}`)}}}
			callsWithTools := 0
			counter := counterFunc(func(_ context.Context, input usage.CountRequest) (usage.CountResult, error) {
				if len(input.Invocation.Tools) > 0 {
					callsWithTools++
					if strings.Contains(messagesText(input.Invocation.Messages), "[Compacted conversation summary.") {
						return counted(50), nil
					}
					return counted(before), nil
				}
				return counted(10), nil
			})
			models := &fakeModel{responses: []model.Response{summaryResponse(`{"summary":"completed","operations":[]}`)}}
			store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
			compactor := newTokenTestCompactor(t, Config{TriggerInputTokens: 100, TargetInputTokens: 50}, counter, models, store)
			result, err := compactor.Compact(context.Background(), contextwindow.CompactionRequest{SessionID: "s", Invocation: request})
			if err != nil {
				t.Fatal(err)
			}
			if result.Changed != (before == 100) || callsWithTools < 2 {
				t.Fatalf("changed=%v full-counts=%d", result.Changed, callsWithTools)
			}
			if before == 100 && len(models.requests) != 1 {
				t.Fatal("exact target should stop after one summary")
			}
		})
	}
}

func TestResolvedRuntimeDefaultsReuseFrozenCheckpoint(t *testing.T) {
	request := firstToolRound()
	request.Provider, request.Model = "", ""
	cfg := Config{TriggerInputTokens: 3000, TargetInputTokens: 1400}
	models := &fakeModel{responses: []model.Response{summaryResponse(`{"summary":"frozen default model summary","operations":[]}`)}}
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	compactor := newTokenTestCompactor(t, cfg, &canonicalTokenCounter{}, models, store)
	first, err := compactor.Compact(context.Background(), contextwindow.CompactionRequest{SessionID: "s", Invocation: request})
	if err != nil {
		t.Fatal(err)
	}
	restarted := newTokenTestCompactor(t, cfg, &canonicalTokenCounter{}, &fakeModel{err: errors.New("must reuse resolved defaults")}, store)
	second, err := restarted.Compact(context.Background(), contextwindow.CompactionRequest{SessionID: "s", Invocation: request})
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("default model reuse failed: %v", err)
	}
	if request.Provider != "" || request.Model != "" {
		t.Fatal("mutated invocation model selection")
	}
}

func TestRollupDiscardsFactsWithoutRewritingRetainedValues(t *testing.T) {
	request := firstToolRound()
	obsolete, _ := json.Marshal(strings.Repeat("obsolete", 230))
	first := `{"summary":"Investigated the build.","operations":[{"op":"set","path":"/keep","value":{"file":"/tmp/build.log","line":7}},{"op":"set","path":"/obsolete","value":` + string(obsolete) + `}]} `
	models := &fakeModel{responses: []model.Response{
		summaryResponse(first), summaryResponse(`{"summary":"Build investigation remains open.","discard_paths":["/obsolete"]}`),
	}}
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	cfg := Config{TriggerInputTokens: 7000, TargetInputTokens: 5000, StateTriggerTokens: 1400, StateTargetTokens: 700}
	compactor := newTokenTestCompactor(t, cfg, &canonicalTokenCounter{}, models, store)
	result, err := compactor.Compact(context.Background(), contextwindow.CompactionRequest{SessionID: "s", Invocation: request})
	if err != nil {
		t.Fatal(err)
	}
	if len(models.requests) != 2 || len(store.entries["s"]) != 2 {
		t.Fatalf("calls=%d entries=%d", len(models.requests), len(store.entries["s"]))
	}
	segment, _ := decodeCheckpoint(store.entries["s"][0].Payload)
	rollup, err := decodeCheckpoint(store.entries["s"][1].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if rollup.Mode != checkpointModeRollup || rollup.Revision != segment.Revision+1 || rollup.CoveredMessages != segment.CoveredMessages {
		t.Fatalf("rollup=%+v", rollup)
	}
	if len(rollup.StateSnapshot) != 1 || rollup.StateSnapshot[0].Path != "/keep" || !rawEqual(rollup.StateSnapshot[0].Value, json.RawMessage(`{"file":"/tmp/build.log","line":7}`)) {
		t.Fatalf("snapshot=%+v", rollup.StateSnapshot)
	}
	if strings.Contains(messagesText(result.Messages), "obsolete") {
		t.Fatal("discarded fact remains in materialized context")
	}
	restarted := newTokenTestCompactor(t, cfg, &canonicalTokenCounter{}, &fakeModel{err: errors.New("must reuse rollup")}, store)
	replay, err := restarted.Compact(context.Background(), contextwindow.CompactionRequest{SessionID: "s", Invocation: request})
	if err != nil || !reflect.DeepEqual(result, replay) {
		t.Fatalf("rollup replay err=%v", err)
	}
}

func TestCheckpointCannotSplitToolRoundAndLegacySequenceAdvances(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "split", true: "legacy"}[legacy], func(t *testing.T) {
			request := firstToolRound()
			cfg := Config{TriggerInputTokens: 3000, TargetInputTokens: 1400}
			models := &fakeModel{responses: []model.Response{
				summaryResponse(`{"summary":"first","operations":[]}`), summaryResponse(`{"summary":"rebuilt","operations":[]}`),
			}}
			store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
			compactor := newTokenTestCompactor(t, cfg, &canonicalTokenCounter{}, models, store)
			if _, err := compactor.Compact(context.Background(), contextwindow.CompactionRequest{SessionID: "s", Invocation: request}); err != nil {
				t.Fatal(err)
			}
			if legacy {
				store.entries["s"][0].Version = 1
			} else {
				checkpoint, _ := decodeCheckpoint(store.entries["s"][0].Payload)
				checkpoint.CoveredMessages = 2
				checkpoint.SourceDigest, _ = messageDigest(request.Messages[1:3])
				store.entries["s"][0].Payload, _ = json.Marshal(checkpoint)
			}
			_, err := compactor.Compact(context.Background(), contextwindow.CompactionRequest{SessionID: "s", Invocation: request})
			if !legacy {
				if !errors.Is(err, ErrCorruptCheckpoint) {
					t.Fatalf("split error=%v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			latest, _ := decodeCheckpoint(store.entries["s"][1].Payload)
			if latest.Sequence != 2 || latest.ParentSequence != 0 {
				t.Fatalf("legacy rebuilt=%+v", latest)
			}
		})
	}
}

func TestCounterFailuresCannotPersistCandidate(t *testing.T) {
	for _, failure := range []string{"count error", "memory error", "changed identity", "disallowed accuracy", "negative count"} {
		t.Run(failure, func(t *testing.T) {
			cause := errors.New("counter unavailable")
			counter := counterFunc(func(_ context.Context, input usage.CountRequest) (usage.CountResult, error) {
				count := counted(100)
				if failure == "disallowed accuracy" {
					return count, nil
				}
				if failure == "negative count" {
					count.InputTokens = -1
					return count, nil
				}
				if strings.Contains(messagesText(input.Invocation.Messages), "[Compacted conversation summary.") {
					if failure == "count error" {
						return usage.CountResult{}, cause
					}
					if failure == "memory error" {
						if len(input.Invocation.Messages) == 1 {
							return usage.CountResult{}, cause
						}
						return counted(50), nil
					}
					count.Source = "changed"
				}
				return count, nil
			})
			cfg := Config{TriggerInputTokens: 100, TargetInputTokens: 50}
			if failure == "disallowed accuracy" {
				cfg.AllowedAccuracies = []usage.Accuracy{usage.AccuracyExact}
			}
			models := &fakeModel{responses: []model.Response{summaryResponse(`{"summary":"done","operations":[]}`)}}
			store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
			compactor := newTokenTestCompactor(t, cfg, counter, models, store)
			_, err := compactor.Compact(context.Background(), contextwindow.CompactionRequest{SessionID: "s", Invocation: firstToolRound()})
			expected := ErrInvalidCount
			if failure == "count error" || failure == "memory error" {
				expected = cause
			}
			if failure == "disallowed accuracy" {
				expected = ErrUnsupportedAccuracy
			}
			if !errors.Is(err, expected) || len(store.entries["s"]) != 0 {
				t.Fatalf("err=%v entries=%d", err, len(store.entries["s"]))
			}
		})
	}
}

func TestLegacyConfigReportsMigrationAndTokenConfigRoundTrips(t *testing.T) {
	for _, field := range []string{"trigger_request_bytes", "target_request_bytes", "summary_chunk_bytes", "anchor_turns", "anchor_rounds", "recent_turns", "max_summary_chunks"} {
		t.Run(field, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, configFileName), []byte(field+" = 2\n"), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := loadConfig(dir)
			if !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), "migrate") {
				t.Fatalf("migration err=%v", err)
			}
		})
	}
	cfg := Config{TriggerInputTokens: 12345, TargetInputTokens: 2345, RecentRounds: 1, SummaryInputTokens: 6000, AllowedAccuracies: []usage.Accuracy{usage.AccuracyEstimate}}
	dir := t.TempDir()
	if err := saveConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadConfig(dir)
	if err != nil || !reflect.DeepEqual(cfg, loaded) {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
}
