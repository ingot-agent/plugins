package contextcompact

import (
	"errors"
	"slices"
	"testing"

	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/tool"
)

func TestGroupRoundsIncludesOnlyCompletedRounds(t *testing.T) {
	t.Parallel()
	user := model.Message{Role: model.RoleUser, Content: content.FromText("continue")}
	answer := model.Message{Role: model.RoleAssistant, Content: content.FromText("done")}
	firstResult := model.Message{Role: model.RoleTool, ToolCallID: "first"}
	secondResult := model.Message{Role: model.RoleTool, ToolCallID: "second"}
	tests := []struct {
		name     string
		messages []model.Message
		want     []roundRange
	}{
		{name: "empty"},
		{name: "user suffix", messages: []model.Message{user, user}},
		{name: "plain answer", messages: []model.Message{user, answer}, want: []roundRange{{0, 2}}},
		{name: "consecutive users", messages: []model.Message{user, user, answer}, want: []roundRange{{0, 3}}},
		{name: "tool results complete round", messages: []model.Message{user, roundToolCalls("first", "second"), firstResult, secondResult}, want: []roundRange{{0, 4}}},
		{name: "awaiting all tools", messages: []model.Message{user, roundToolCalls("first", "second")}},
		{name: "partial tool results", messages: []model.Message{user, roundToolCalls("first", "second"), firstResult}},
		{name: "multiple rounds within turn", messages: []model.Message{user, roundToolCalls("first"), firstResult, answer}, want: []roundRange{{0, 3}, {3, 4}}},
		{name: "new users belong to next round", messages: []model.Message{user, answer, user, user, answer}, want: []roundRange{{0, 2}, {2, 5}}},
		{name: "completed prefix and user suffix", messages: []model.Message{user, answer, user, user}, want: []roundRange{{0, 2}}},
		{name: "completed prefix and partial tools", messages: []model.Message{user, answer, roundToolCalls("first", "second"), firstResult}, want: []roundRange{{0, 2}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := groupRounds(test.messages)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, test.want) {
				t.Fatalf("rounds=%v, want %v", got, test.want)
			}
		})
	}
}

func TestCompletedRoundBoundariesRemainStableAsHistoryGrows(t *testing.T) {
	t.Parallel()
	user := model.Message{Role: model.RoleUser}
	messages := []model.Message{
		user,
		roundToolCalls("first", "second"),
		{Role: model.RoleTool, ToolCallID: "first"},
		{Role: model.RoleTool, ToolCallID: "second"},
		{Role: model.RoleAssistant},
		user,
		user,
		roundToolCalls("third"),
		{Role: model.RoleTool, ToolCallID: "third"},
		roundToolCalls("fourth"),
		{Role: model.RoleTool, ToolCallID: "fourth"},
		user,
	}
	completed := []roundRange{{0, 4}, {4, 5}, {5, 9}, {9, 11}}
	for size := 0; size <= len(messages); size++ {
		got, err := groupRounds(messages[:size])
		if err != nil {
			t.Fatalf("prefix length %d: %v", size, err)
		}
		var want []roundRange
		for _, round := range completed {
			if round.end <= size {
				want = append(want, round)
			}
		}
		if !slices.Equal(got, want) {
			t.Fatalf("prefix length %d: rounds=%v, want %v", size, got, want)
		}
	}
}

func TestInspectRequestPreservesRecentCompleteRoundsAndOpenSuffix(t *testing.T) {
	t.Parallel()
	request := model.Request{Messages: []model.Message{
		{Role: model.RoleSystem},
		{Role: model.RoleUser},
		roundToolCalls("first"),
		{Role: model.RoleTool, ToolCallID: "first"},
		{Role: model.RoleAssistant},
		{Role: model.RoleUser},
		{Role: model.RoleAssistant},
		roundToolCalls("pending"),
	}}
	for _, test := range []struct {
		recent      int
		eligibleEnd int
	}{
		{recent: 0, eligibleEnd: 6},
		{recent: 1, eligibleEnd: 4},
		{recent: 2, eligibleEnd: 3},
		{recent: 3, eligibleEnd: 0},
		{recent: 10, eligibleEnd: 0},
	} {
		layout, err := inspectRequest(request, test.recent)
		if err != nil {
			t.Fatal(err)
		}
		if len(layout.system) != 1 || len(layout.conversation) != 7 || layout.completeEnd != 6 || layout.eligibleEnd != test.eligibleEnd {
			t.Fatalf("recent=%d: layout=%+v", test.recent, layout)
		}
		if !slices.Equal(layout.rounds, []roundRange{{0, 3}, {3, 4}, {4, 6}}) {
			t.Fatalf("recent=%d: rounds=%v", test.recent, layout.rounds)
		}
		for end := -1; end <= len(layout.conversation)+1; end++ {
			wantBoundary := end == 0 || end == 3 || end == 4 || end == 6
			if got := isRoundBoundary(layout, end); got != wantBoundary {
				t.Fatalf("boundary %d=%v, want %v", end, got, wantBoundary)
			}
		}
	}
}

func TestInspectRequestWithNoCompleteRound(t *testing.T) {
	t.Parallel()
	for _, messages := range [][]model.Message{
		nil,
		{{Role: model.RoleSystem}},
		{{Role: model.RoleUser}},
		{{Role: model.RoleUser}, roundToolCalls("pending")},
	} {
		layout, err := inspectRequest(model.Request{Messages: messages}, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(layout.rounds) != 0 || layout.completeEnd != 0 || layout.eligibleEnd != 0 {
			t.Fatalf("layout=%+v", layout)
		}
		if !isRoundBoundary(layout, 0) || isRoundBoundary(layout, 1) {
			t.Fatalf("unexpected empty-history boundary: %+v", layout)
		}
	}
}

func TestGroupRoundsRejectsInvalidToolOrdering(t *testing.T) {
	t.Parallel()
	user := model.Message{Role: model.RoleUser}
	answer := model.Message{Role: model.RoleAssistant}
	firstResult := model.Message{Role: model.RoleTool, ToolCallID: "first"}
	secondResult := model.Message{Role: model.RoleTool, ToolCallID: "second"}
	tests := []struct {
		name     string
		messages []model.Message
	}{
		{name: "missing initial user", messages: []model.Message{answer}},
		{name: "orphan tool", messages: []model.Message{user, firstResult}},
		{name: "tool after plain answer", messages: []model.Message{user, answer, firstResult}},
		{name: "out of order tools", messages: []model.Message{user, roundToolCalls("first", "second"), secondResult, firstResult}},
		{name: "duplicate result", messages: []model.Message{user, roundToolCalls("first", "second"), firstResult, firstResult}},
		{name: "extra result", messages: []model.Message{user, roundToolCalls("first"), firstResult, firstResult}},
		{name: "user before tools", messages: []model.Message{user, roundToolCalls("first"), user}},
		{name: "assistant before tools", messages: []model.Message{user, roundToolCalls("first"), answer}},
		{name: "user during tools", messages: []model.Message{user, roundToolCalls("first", "second"), firstResult, user}},
		{name: "assistant during tools", messages: []model.Message{user, roundToolCalls("first", "second"), firstResult, answer}},
		{name: "duplicate calls", messages: []model.Message{user, roundToolCalls("first", "first")}},
		{name: "system within conversation", messages: []model.Message{user, {Role: model.RoleSystem}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := groupRounds(test.messages); !errors.Is(err, ErrInvalidHistory) {
				t.Fatalf("error=%v, want ErrInvalidHistory", err)
			}
		})
	}
}

func roundToolCalls(ids ...string) model.Message {
	calls := make([]tool.Call, len(ids))
	for i, id := range ids {
		calls[i] = tool.Call{ID: id, Name: "read", Arguments: []byte(`{}`)}
	}
	return model.Message{Role: model.RoleAssistant, ToolCalls: calls}
}
