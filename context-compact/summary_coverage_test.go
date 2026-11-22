package contextcompact

import (
	"context"
	"strings"
	"testing"

	"github.com/ingot-agent/plugins/context-compact/internal/engine"
	"github.com/ingot-agent/sdk/contextwindow"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/tool"
)

// TestSummaryInputRepresentsEveryRound locks the rule that no dropped round is
// left out of the summary. Skipping one would remove it from the request while
// no summary mentions it, which is the only unrecoverable outcome compaction can
// produce.
func TestSummaryInputRepresentsEveryRound(t *testing.T) {
	request := model.Request{Provider: "p", Model: "m", Messages: []model.Message{
		{Role: model.RoleSystem, Content: text("s")},
	}}
	big := strings.Repeat("x", maxSummaryInputTokens*4)
	for i := 0; i < 3; i++ {
		request.Messages = append(request.Messages, userMessage(big), assistantMessage("ok"))
	}
	layout := inspect(t, request)
	prompt, included := buildSummaryInput(layout, layout.Rounds, record{})
	if included != len(layout.Rounds) {
		t.Fatalf("included %d of %d rounds", included, len(layout.Rounds))
	}
	// The prompt must stay within the summarizer budget.
	if got := engine.EstimateText(prompt) + engine.EstimateText(summarySystemPrompt); got > maxSummaryInputTokens {
		t.Fatalf("prompt uses %d tokens, over the %d budget", got, maxSummaryInputTokens)
	}
}

// TestCompactAdvancesCoverageOnlyForSummarizedRounds: a summary that could not
// carry every dropped round must not mark them all as covered.
func TestCompactAdvancesCoverageOnlyForSummarizedRounds(t *testing.T) {
	store := newMemoryStore()
	huge := strings.Repeat("n", maxSummaryInputTokens*4)
	modelRuntime := &fakeModel{onCall: func(request model.Request) (model.Response, error) {
		return summaryResponse(summaryText("partial summary")), nil
	}}
	request := model.Request{Provider: "p", Model: "m", Messages: []model.Message{
		{Role: model.RoleSystem, Content: text("system")},
		userMessage(huge), assistantMessage(huge),
		userMessage(huge), assistantMessage(huge),
		userMessage("tail"), assistantMessage("done"),
	}}
	full := engine.TokensOf(request.Messages)
	c := newCompactor(t, Config{TriggerTokens: full / 2, TargetTokens: 1000}, modelRuntime, store)

	result, err := c.Compact(context.Background(), contextwindow.CompactionRequest{
		RootSessionID: "s", SessionID: "s", Invocation: request,
	})
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if len(result.Messages) == 0 {
		t.Fatal("expected a usable result")
	}
	layout := inspect(t, request)
	rec, rounds := c.loadRecord(context.Background(), "s", layout)
	if rec.Summary == "" {
		t.Fatal("expected a persisted record")
	}
	if rounds >= len(layout.Rounds) {
		t.Fatalf("coverage %d claims every round although the summary could not carry them", rounds)
	}
	// Everything after the covered prefix must still be present.
	if !containsText(result.Messages, "done") {
		t.Fatal("the newest narrative was lost")
	}
}

// TestSummarizeReportsProviderFailures maps failures to reasons for telemetry.
func TestSummarizeReportsProviderFailures(t *testing.T) {
	request := model.Request{Provider: "p", Model: "m", Messages: []model.Message{
		{Role: model.RoleSystem, Content: text("s")},
		userMessage("u"), assistantMessage("a"),
		userMessage("u2"), assistantMessage("a2"),
	}}
	layout := inspect(t, request)
	cfg := normalizedConfig{summaryMaxTokens: 128, summaryMaxBytes: 1024, reasoningEffort: model.ReasoningEffortNone}
	for name, modelRuntime := range map[string]*fakeModel{
		"provider error": {onCall: func(model.Request) (model.Response, error) {
			return model.Response{}, context.DeadlineExceeded
		}},
		"empty": {onCall: func(model.Request) (model.Response, error) {
			return summaryResponse(""), nil
		}},
		"tool calls": {onCall: func(model.Request) (model.Response, error) {
			return model.Response{Message: model.Message{Role: model.RoleAssistant, ToolCalls: []tool.Call{
				toolCall("c", "shell", `{}`),
			}}}, nil
		}},
	} {
		t.Run(name, func(t *testing.T) {
			c := &compactor{model: modelRuntime, logger: discardLogger()}
			outcome := c.summarize(context.Background(),
				contextwindow.CompactionRequest{RootSessionID: "s", SessionID: "s", Invocation: request},
				record{}, layout.Rounds, layout, cfg)
			if outcome.OK {
				t.Fatal("expected a failure")
			}
			if outcome.Reason == "" {
				t.Fatal("failure must carry a reason")
			}
			if outcome.CoveredRounds != 0 {
				t.Fatal("a failed summary must not claim coverage")
			}
		})
	}
}
