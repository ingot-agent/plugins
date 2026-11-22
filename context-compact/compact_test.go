package contextcompact

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ingot-agent/plugins/context-compact/internal/engine"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/contextwindow"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/tool"
)

func newCompactor(t *testing.T, cfg Config, modelRuntime model.Runtime, store session.Store) *compactor {
	t.Helper()
	exports, cleanup, err := newTestCompactor(context.Background(), testDependencies{
		Model: modelRuntime,
		Store: store,
		State: testStateScope{dir: writeTestConfig(t, cfg)},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if cleanup != nil {
		t.Cleanup(func() { _ = cleanup(context.Background()) })
	}
	return exports.Compactor.(*compactor)
}

func compact(t *testing.T, c *compactor, request model.Request) contextwindow.CompactionResult {
	t.Helper()
	result, err := c.Compact(context.Background(), contextwindow.CompactionRequest{
		RootSessionID: "s", SessionID: "s", Invocation: request,
	})
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	return result
}

func tokensOf(messages []model.Message) int64 { return engine.TokensOf(messages) }

// TestCompactNeverFailsOnSummarizerFailures is the central guarantee of v2.
// Every one of these cases terminated the turn in v1.
func TestCompactNeverFailsOnSummarizerFailures(t *testing.T) {
	cases := map[string]*fakeModel{
		"empty content": {onCall: func(request model.Request) (model.Response, error) {
			return model.Response{
				Message:      model.Message{Role: model.RoleAssistant, Content: text("")},
				FinishReason: "length",
			}, nil
		}},
		"whitespace only": {onCall: func(request model.Request) (model.Response, error) {
			return summaryResponse("   \n\t "), nil
		}},
		"provider error": {onCall: func(request model.Request) (model.Response, error) {
			return model.Response{}, errors.New("boom")
		}},
		"context cancellation is not a summarizer fault": {onCall: func(request model.Request) (model.Response, error) {
			return model.Response{}, context.DeadlineExceeded
		}},
		"tool calls instead of text": {onCall: func(request model.Request) (model.Response, error) {
			return model.Response{Message: model.Message{
				Role: model.RoleAssistant,
				ToolCalls: []tool.Call{
					toolCall("c1", "shell", `{"cmd":"ls"}`),
				},
			}}, nil
		}},
		"truncated to nothing": {onCall: func(request model.Request) (model.Response, error) {
			return model.Response{
				Message:      model.Message{Role: model.RoleAssistant, Content: text("SUM")},
				FinishReason: "length",
			}, nil
		}},
	}

	for name, modelRuntime := range cases {
		t.Run(name, func(t *testing.T) {
			store := newMemoryStore()
			c := newCompactor(t, Config{TriggerTokens: 100, TargetTokens: 50}, modelRuntime, store)
			request := toolRequest(6, 400)
			result := compact(t, c, request)

			if tokensOf(result.Messages) > 50 {
				t.Fatalf("output %d tokens exceeds the target", tokensOf(result.Messages))
			}
			if !result.Changed {
				t.Fatal("expected a compacted result")
			}
			if err := validateStructure(result.Messages); err != nil {
				t.Fatalf("%v", err)
			}
			// A failed summarizer must not invent durable state.
			for _, entry := range mustLoad(t, store, "s") {
				if entry.Kind == recordKind {
					t.Fatalf("record persisted despite summary failure: %s", entry.Payload)
				}
			}
		})
	}
}

// TestCompactKeepsNarrativeWhenElisionSuffices checks that the summarizer is
// not even called when the ladder can reach the target.
func TestCompactKeepsNarrativeWhenElisionSuffices(t *testing.T) {
	modelRuntime := &fakeModel{}
	store := newMemoryStore()
	request := toolRequest(6, 2000)
	full := engine.TokensOf(request.Messages)
	target := full / 2
	c := newCompactor(t, Config{TriggerTokens: target + 1, TargetTokens: target}, modelRuntime, store)

	result := compact(t, c, request)
	if len(modelRuntime.calls()) != 0 {
		t.Fatalf("summarizer called %d times although elision sufficed", len(modelRuntime.calls()))
	}
	if tokensOf(result.Messages) > target {
		t.Fatalf("output %d exceeds target %d", tokensOf(result.Messages), target)
	}
	for i := 0; i < 6; i++ {
		want := "step prompt"
		if !containsText(result.Messages, want) {
			t.Fatal("user narrative lost")
		}
	}
	if err := validateStructure(result.Messages); err != nil {
		t.Fatal(err)
	}
}

// TestCompactSummarizesDroppedRoundsAndPersistsCoverage covers the chained path:
// drop, summarize, restore, persist.
func TestCompactSummarizesDroppedRoundsAndPersistsCoverage(t *testing.T) {
	store := newMemoryStore()
	modelRuntime := &fakeModel{onCall: func(request model.Request) (model.Response, error) {
		return summaryResponse(summaryText("earlier work: the build passed")), nil
	}}
	// Narrative-only rounds cannot be elided, forcing drops.
	request := simpleRequest()
	for i := 0; i < 12; i++ {
		request.Messages = append(request.Messages,
			userMessage("prompt "+strings.Repeat("p", 400)),
			assistantMessage("answer "+strings.Repeat("a", 400)),
		)
	}
	full := engine.TokensOf(request.Messages)
	c := newCompactor(t, Config{TriggerTokens: full / 2, TargetTokens: full / 3}, modelRuntime, store)

	result := compact(t, c, request)
	if tokensOf(result.Messages) > full/3 {
		t.Fatalf("output %d exceeds target %d", tokensOf(result.Messages), full/3)
	}
	if len(modelRuntime.calls()) == 0 {
		t.Fatal("expected a summarizer call")
	}
	// The summary must appear in the compacted request.
	if !containsText(result.Messages, "earlier work: the build passed") &&
		!containsSubstring(result.Messages, "earlier work: the build passed") {
		t.Fatal("summary missing from the compacted request")
	}
	entries := mustLoad(t, store, "s")
	records := 0
	for _, entry := range entries {
		if entry.Kind == recordKind {
			records++
		}
	}
	if records != 1 {
		t.Fatalf("records = %d, want 1", records)
	}
	if err := validateStructure(result.Messages); err != nil {
		t.Fatal(err)
	}
}

// TestCompactReusesCoverageOnNextCall: the second call must not re-summarize.
func TestCompactReusesCoverageOnNextCall(t *testing.T) {
	store := newMemoryStore()
	modelRuntime := &fakeModel{onCall: func(request model.Request) (model.Response, error) {
		return summaryResponse(summaryText("summary one")), nil
	}}
	request := simpleRequest()
	for i := 0; i < 12; i++ {
		request.Messages = append(request.Messages,
			userMessage("prompt "+strings.Repeat("p", 400)),
			assistantMessage("answer "+strings.Repeat("a", 400)),
		)
	}
	full := engine.TokensOf(request.Messages)
	c := newCompactor(t, Config{TriggerTokens: full / 2, TargetTokens: full / 3}, modelRuntime, store)

	first := compact(t, c, request)
	callsAfterFirst := len(modelRuntime.calls())
	if callsAfterFirst == 0 {
		t.Fatal("expected a summarizer call on the first compaction")
	}

	// Grow the conversation by one short round and compact again.
	grown := request
	grown.Messages = append(append([]model.Message(nil), request.Messages...),
		userMessage("next"), assistantMessage("ok"))
	second := compact(t, c, grown)
	if len(modelRuntime.calls()) != callsAfterFirst {
		t.Fatalf("summarizer called again although coverage was reused: %d -> %d",
			callsAfterFirst, len(modelRuntime.calls()))
	}
	if !containsSubstring(second.Messages, "summary one") {
		t.Fatal("stored summary not applied")
	}
	_ = first
	if err := validateStructure(second.Messages); err != nil {
		t.Fatal(err)
	}
}

// TestCompactBelowTriggerStillAppliesCoverage: below the trigger the previous
// summary must still replace the rounds it covers, otherwise history would be
// re-sent in full.
func TestCompactBelowTriggerStillAppliesCoverage(t *testing.T) {
	store := newMemoryStore()
	modelRuntime := &fakeModel{onCall: func(request model.Request) (model.Response, error) {
		return summaryResponse(summaryText("durable summary text")), nil
	}}
	request := simpleRequest()
	for i := 0; i < 12; i++ {
		request.Messages = append(request.Messages,
			userMessage("prompt "+strings.Repeat("p", 400)),
			assistantMessage("answer "+strings.Repeat("a", 400)),
		)
	}
	full := engine.TokensOf(request.Messages)
	c := newCompactor(t, Config{TriggerTokens: full / 2, TargetTokens: full / 3}, modelRuntime, store)
	compact(t, c, request)

	// Raise the trigger well above the request size through a reload.
	if err := saveConfig(c.loader.scope, Config{TriggerTokens: full * 10, TargetTokens: full * 5}); err != nil {
		t.Fatal(err)
	}
	below := compact(t, c, request)
	if !containsSubstring(below.Messages, "durable summary text") {
		t.Fatal("coverage was dropped when below the trigger")
	}
	if tokensOf(below.Messages) >= full {
		t.Fatalf("expected the summary to replace covered rounds: %d >= %d", tokensOf(below.Messages), full)
	}
}

// TestCompactIgnoresStaleCoverage: rewriting history must invalidate a record
// silently instead of failing.
func TestCompactIgnoresStaleCoverage(t *testing.T) {
	store := newMemoryStore()
	modelRuntime := &fakeModel{onCall: func(request model.Request) (model.Response, error) {
		return summaryResponse(summaryText("stale summary marker")), nil
	}}
	request := simpleRequest()
	for i := 0; i < 12; i++ {
		request.Messages = append(request.Messages,
			userMessage("prompt "+strings.Repeat("p", 400)),
			assistantMessage("answer "+strings.Repeat("a", 400)),
		)
	}
	full := engine.TokensOf(request.Messages)
	c := newCompactor(t, Config{TriggerTokens: full / 2, TargetTokens: full / 3}, modelRuntime, store)
	compact(t, c, request)

	// Change one covered message: the digest no longer matches.
	changed := request
	changed.Messages = append([]model.Message(nil), request.Messages...)
	changed.Messages[1] = userMessage("a different prompt")

	result := compact(t, c, changed)
	if err := validateStructure(result.Messages); err != nil {
		t.Fatal(err)
	}
	if tokensOf(result.Messages) > full/3 {
		t.Fatalf("output %d exceeds target", tokensOf(result.Messages))
	}
}

// TestCompactPreservesReplayOfUntouchedMessages locks the SDK contract.
func TestCompactPreservesReplayOfUntouchedMessages(t *testing.T) {
	replay := &model.ReplayState{Format: "f", Provider: "p", Model: "m", Data: []byte("opaque-state")}
	request := simpleRequest("first", "answer")
	request.Messages = append(request.Messages, userMessage("second"), model.Message{
		Role: model.RoleAssistant, Content: text("second answer"), Replay: replay,
	})
	c := newCompactor(t, Config{TriggerTokens: 1_000_000, TargetTokens: 500_000}, &fakeModel{}, newMemoryStore())

	result := compact(t, c, request)
	var found *model.ReplayState
	for _, message := range result.Messages {
		if message.Replay != nil {
			found = message.Replay
		}
	}
	if found == nil {
		t.Fatal("Replay state lost from an untouched message")
	}
	if string(found.Data) != "opaque-state" {
		t.Fatalf("Replay data changed: %q", found.Data)
	}
	if result.Changed {
		t.Fatal("no compaction should have happened")
	}
}

// TestCompactReturnsStructuralErrorOnly: a broken history is reported.
func TestCompactReturnsStructuralErrorOnly(t *testing.T) {
	c := newCompactor(t, Config{}, &fakeModel{}, newMemoryStore())
	broken := model.Request{Messages: []model.Message{
		{Role: model.RoleSystem, Content: text("s")},
		userMessage("hi"),
		toolMessage("call_9", "shell", "orphan"),
	}}
	if _, err := c.Compact(context.Background(), contextwindow.CompactionRequest{
		RootSessionID: "s", SessionID: "s", Invocation: broken,
	}); !errors.Is(err, engine.ErrInvalidHistory) {
		t.Fatalf("error = %v, want ErrInvalidHistory", err)
	}
}

// TestCompactReportsUncompactableWithoutFailing: a system prompt larger than the
// target degrades and still returns a usable request.
func TestCompactReportsUncompactableWithoutFailing(t *testing.T) {
	huge := model.Message{Role: model.RoleSystem, Content: text(strings.Repeat("s", 20_000))}
	request := model.Request{Provider: "p", Model: "m", Messages: []model.Message{
		huge, userMessage("hello"), assistantMessage("ok"),
	}}
	c := newCompactor(t, Config{TriggerTokens: 100, TargetTokens: 50}, &fakeModel{}, newMemoryStore())
	result := compact(t, c, request)
	if len(result.Messages) == 0 {
		t.Fatal("expected a usable message list")
	}
}

// TestCompactStoresNothingWhenHistoryFits: an undersized request must be free.
func TestCompactStoresNothingWhenHistoryFits(t *testing.T) {
	store := newMemoryStore()
	modelRuntime := &fakeModel{}
	c := newCompactor(t, Config{}, modelRuntime, store)
	result := compact(t, c, simpleRequest("a", "b"))
	if result.Changed {
		t.Fatal("Changed must be false when nothing was compacted")
	}
	if len(modelRuntime.calls()) != 0 || store.count() != 0 {
		t.Fatal("no work should happen under the trigger")
	}
}

func mustLoad(t *testing.T, store *memoryStore, id session.ID) []session.Entry {
	t.Helper()
	entries, err := store.Load(context.Background(), id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return entries
}

func containsText(messages []model.Message, want string) bool {
	for _, message := range messages {
		if engine.TextOf(message) == want {
			return true
		}
	}
	return false
}

func containsSubstring(messages []model.Message, want string) bool {
	for _, message := range messages {
		if strings.Contains(engine.TextOf(message), want) {
			return true
		}
	}
	return false
}

// validateStructure mirrors the provider's message-sequence rules.
func validateStructure(messages []model.Message) error {
	var pending []string
	matched := 0
	for i, message := range messages {
		if message.Role == model.RoleSystem && i != 0 {
			return errors.New("system message after the first position")
		}
		if message.Role == model.RoleTool {
			if len(message.Content) > 0 && !strings.Contains(engine.TextOf(message), "elided") &&
				engine.TextOf(message) == "" {
				return errors.New("empty tool content")
			}
		}
		switch message.Role {
		case model.RoleUser:
			if len(pending) != matched {
				return errors.New("user message interrupts a tool exchange")
			}
		case model.RoleAssistant:
			if len(pending) != matched {
				return errors.New("assistant message interrupts a tool exchange")
			}
			pending = pending[:0]
			matched = 0
			for _, call := range message.ToolCalls {
				pending = append(pending, call.ID)
			}
		case model.RoleTool:
			if matched >= len(pending) || message.ToolCallID != pending[matched] {
				return errors.New("tool result has no matching call")
			}
			matched++
		}
	}
	return nil
}

var _ = content.FromText
