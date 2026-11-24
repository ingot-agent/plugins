package engine

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/tool"
)

// fixtureMessage mirrors one stored session message. Session storage keeps tool
// arguments base64-encoded, so the loader accepts the decoded form and treats
// missing arguments as an empty object.
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
	// A real request carries the system prompt from prompt.default.
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

// TestRealSessionFitsTargets is the regression that motivated v2: a real
// 3.9 MB session saturated five rounds of LLM summarization and still failed.
// The deterministic engine must fit the same history with no model calls.
func TestRealSessionFitsTargets(t *testing.T) {
	messages := loadFixture(t)
	layout, err := Inspect(model.Request{Messages: messages})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	t.Logf("rounds=%d tail=%d messages=%d", len(layout.Rounds), len(layout.Tail()), len(messages))

	full := Shrink(layout, Config{TargetTokens: 1 << 40})
	t.Logf("full fidelity: %d tokens", full.Tokens)
	if full.Tokens < 500_000 {
		t.Fatalf("fixture looks too small to be the regression case: %d", full.Tokens)
	}

	before := narrative(full.Messages)
	for _, protect := range []int64{0, 100_000, 200_000} {
		result := Shrink(layout, Config{TargetTokens: 250_000, ProtectRecentTokens: protect})
		if result.Tokens > 250_000 {
			t.Fatalf("protect=%d: tokens %d exceed target", protect, result.Tokens)
		}
		if result.Uncompactable {
			t.Fatalf("protect=%d: unexpectedly uncompactable", protect)
		}
		levels := map[Level]int{}
		for _, level := range result.Levels {
			levels[level]++
		}
		t.Logf("protect=%d -> %d tokens, dropped=%d elided=%d full=%d",
			protect, result.Tokens, levels[LevelDropped], result.Elided, levels[LevelFull])

		if len(result.Dropped) == 0 {
			after := narrative(result.Messages)
			if strings.Join(before, "\x00") != strings.Join(after, "\x00") {
				t.Fatalf("protect=%d: narrative changed without dropping a round", protect)
			}
		}
		if err := validateSequence(result.Messages); err != nil {
			t.Fatalf("protect=%d: %v", protect, err)
		}
	}
}

func narrative(messages []model.Message) []string {
	var out []string
	for _, message := range messages {
		switch message.Role {
		case model.RoleUser, model.RoleAssistant:
			if text := TextOf(message); text != "" {
				out = append(out, string(message.Role)+":"+text)
			}
		}
	}
	return out
}

// BenchmarkRealSessionShrink measures the deterministic path on a history that
// previously required ten model calls and still failed.
func BenchmarkRealSessionShrink(b *testing.B) {
	path := os.Getenv("COMPACT_FIXTURE")
	if path == "" {
		b.Skip("set COMPACT_FIXTURE to benchmark against a real session")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		b.Fatalf("read fixture: %v", err)
	}
	var input []fixtureMessage
	if err := json.Unmarshal(raw, &input); err != nil {
		b.Fatalf("decode fixture: %v", err)
	}
	messages := make([]model.Message, 0, len(input))
	for _, item := range input {
		parts := make(content.Content, 0, len(item.Content))
		for _, part := range item.Content {
			if part.Kind == "text" {
				parts = append(parts, content.Part{Kind: content.KindText, Text: part.Text})
			}
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
	request := model.Request{Messages: messages}
	layout, err := Inspect(request)
	if err != nil {
		b.Fatalf("Inspect: %v", err)
	}
	cfg := Config{TargetTokens: 250_000, ProtectRecentTokens: 100_000}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if result := Shrink(layout, cfg); result.Uncompactable {
			b.Fatal("unexpectedly uncompactable")
		}
	}
}
