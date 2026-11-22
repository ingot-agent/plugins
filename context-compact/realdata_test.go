package contextcompact

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/ingot-agent/plugins/context-compact/internal/engine"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/contextwindow"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/tool"
)

// fixtureMessage mirrors one stored session message.
type fixtureMessage struct {
	Role    string `json:"role"`
	Content []struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	} `json:"content"`
	Name       string `json:"name"`
	ToolCallID string `json:"tool_call_id"`
	ToolCalls  []struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"tool_calls"`
}

// loadFixture reads a session message export. Set COMPACT_FIXTURE to the path of
// a JSON array of stored messages; without it the regression is skipped.
func loadFixture(t *testing.T) []model.Message {
	t.Helper()
	path := os.Getenv("COMPACT_FIXTURE")
	if path == "" {
		t.Skip("set COMPACT_FIXTURE to a session message export to run the real-data regression")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var input []fixtureMessage
	if err := json.Unmarshal(raw, &input); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	messages := make([]model.Message, 0, len(input)+1)
	messages = append(messages, model.Message{
		Role:    model.RoleSystem,
		Content: content.FromText(strings.Repeat("system prompt line\n", 400)),
	})
	for _, item := range input {
		parts := make(content.Content, 0, len(item.Content))
		for _, part := range item.Content {
			if part.Kind == "text" {
				parts = append(parts, content.Part{Kind: content.KindText, Text: part.Text})
				continue
			}
			parts = append(parts, content.Part{Kind: content.KindImage})
		}
		calls := make([]tool.Call, 0, len(item.ToolCalls))
		for _, call := range item.ToolCalls {
			arguments := call.Arguments
			if arguments == "" {
				arguments = "{}"
			}
			calls = append(calls, tool.Call{ID: call.ID, Name: call.Name, Arguments: json.RawMessage(arguments)})
		}
		messages = append(messages, model.Message{
			Role: model.Role(item.Role), Content: parts,
			Name: item.Name, ToolCallID: item.ToolCallID, ToolCalls: calls,
		})
	}
	return messages
}

// TestRealSessionCompactsWithoutSummarizer is the end-to-end regression for the
// session that defeated v1: a 3.9 MB history must fit the target using only the
// deterministic ladder, with no model call and no error.
func TestRealSessionCompactsWithoutSummarizer(t *testing.T) {
	messages := loadFixture(t)
	request := model.Request{Provider: "p", Model: "m", Messages: messages}
	full := engine.TokensOf(messages)
	t.Logf("full fidelity: %d tokens, %d messages", full, len(messages))

	modelRuntime := &fakeModel{}
	store := newMemoryStore()
	c := newCompactor(t, Config{
		TriggerTokens:       full - 1,
		TargetTokens:        250_000,
		ProtectRecentTokens: 100_000,
	}, modelRuntime, store)

	result, err := c.Compact(context.Background(), contextwindow.CompactionRequest{
		RootSessionID: "s", SessionID: "s", Invocation: request,
	})
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	after := engine.TokensOf(result.Messages)
	t.Logf("compacted: %d tokens (%d%% of full), %d messages", after, after*100/full, len(result.Messages))
	if after > 250_000 {
		t.Fatalf("tokens %d exceed target", after)
	}
	if len(modelRuntime.calls()) != 0 {
		t.Fatalf("summarizer was called %d times although elision sufficed", len(modelRuntime.calls()))
	}
	if store.count() != 0 {
		t.Fatalf("a summary record was written although nothing was dropped")
	}
	if err := validateStructure(result.Messages); err != nil {
		t.Fatalf("%v", err)
	}
	assertNarrativePreserved(t, messages, result.Messages)
}

// assertNarrativePreserved verifies that every user and assistant message text
// in the compacted output is byte-identical to its original.
func assertNarrativePreserved(t *testing.T, before, after []model.Message) {
	t.Helper()
	original := make(map[string]struct{}, len(before))
	for _, message := range before {
		switch message.Role {
		case model.RoleUser, model.RoleAssistant:
			if text := engine.TextOf(message); text != "" {
				original[text] = struct{}{}
			}
		}
	}
	for _, message := range after {
		switch message.Role {
		case model.RoleUser, model.RoleAssistant:
			text := engine.TextOf(message)
			if text == "" {
				continue
			}
			if _, ok := original[text]; !ok {
				t.Fatalf("narration changed: %.60q", text)
			}
		}
	}
}

// TestRealSessionSummarizesWhenTargetIsTiny forces the summary path on the real
// history: with a target far below what elision can reach, rounds must be
// dropped, summarized, and persisted, and the result must still fit.
func TestRealSessionSummarizesWhenTargetIsTiny(t *testing.T) {
	messages := loadFixture(t)
	request := model.Request{Provider: "p", Model: "m", Messages: messages}
	full := engine.TokensOf(messages)

	modelRuntime := &fakeModel{onCall: func(request model.Request) (model.Response, error) {
		return summaryResponse(summaryText("A long refactor of a Go backend: dead packages were removed, docs rewritten, and tests consolidated.")), nil
	}}
	store := newMemoryStore()
	// Narrative alone costs ~85k tokens for this history, so only a target below
	// that can force drops.
	target := int64(40_000)
	c := newCompactor(t, Config{
		TriggerTokens:       full / 2,
		TargetTokens:        target,
		ProtectRecentTokens: 5_000,
	}, modelRuntime, store)

	result, err := c.Compact(context.Background(), contextwindow.CompactionRequest{
		RootSessionID: "s", SessionID: "s", Invocation: request,
	})
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	after := engine.TokensOf(result.Messages)
	t.Logf("full=%d target=%d after=%d summarizer_calls=%d records=%d",
		full, target, after, len(modelRuntime.calls()), store.count())
	if after > target {
		t.Fatalf("tokens %d exceed target %d", after, target)
	}
	if len(modelRuntime.calls()) == 0 {
		t.Fatal("expected the summarizer to be used")
	}
	if store.count() == 0 {
		t.Fatal("expected a summary record")
	}
	if err := validateStructure(result.Messages); err != nil {
		t.Fatalf("%v", err)
	}

	// A second call must reuse the record instead of summarizing again.
	callsAfterFirst := len(modelRuntime.calls())
	again, err := c.Compact(context.Background(), contextwindow.CompactionRequest{
		RootSessionID: "s", SessionID: "s", Invocation: request,
	})
	if err != nil {
		t.Fatalf("second Compact: %v", err)
	}
	if len(modelRuntime.calls()) > callsAfterFirst+1 {
		t.Fatalf("summarizer called %d more times on an unchanged history",
			len(modelRuntime.calls())-callsAfterFirst)
	}
	if engine.TokensOf(again.Messages) > target {
		t.Fatalf("second result %d exceeds target", engine.TokensOf(again.Messages))
	}
}
