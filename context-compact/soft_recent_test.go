package contextcompact

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ingot-agent/sdk/contextwindow"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/tool"
	"github.com/ingot-agent/sdk/usage"
)

func TestSoftRecentExpandsUnprofitableOldRoundIntoCompleteRecentRound(t *testing.T) {
	t.Parallel()
	request := shortHistoryWithLargeRecentRound()
	original := cloneRequest(request)
	models := &fakeModel{responses: []model.Response{
		summaryResponse(`{"summary":"` + strings.Repeat("Old greeting detail. ", 100) + `","operations":[]}`),
		summaryResponse(`{"summary":"The failure is in /tmp/build.log; continue the investigation.","operations":[]}`),
	}}
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	compactor := newTokenTestCompactor(t, Config{
		TriggerInputTokens: 3000, TargetInputTokens: 1500, RecentRounds: 1,
	}, &canonicalTokenCounter{}, models, store)
	result, err := compactor.Compact(context.Background(), contextwindow.CompactionRequest{SessionID: "s", Invocation: request})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || len(models.requests) != 2 || len(store.entries["s"]) != 1 {
		t.Fatalf("changed=%v calls=%d entries=%d", result.Changed, len(models.requests), len(store.entries["s"]))
	}
	for i, wantSource := range [][]model.Message{request.Messages[1:3], request.Messages[1:]} {
		var input segmentInput
		if err := json.Unmarshal([]byte(messageText(models.requests[i].Messages[1])), &input); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(input.Source, projectMessages(wantSource)) {
			t.Fatalf("call %d did not contain the complete expected source rounds", i)
		}
	}
	checkpoint, err := decodeCheckpoint(store.entries["s"][0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.Sequence != 1 || checkpoint.ParentSequence != 0 || checkpoint.Revision != 1 || checkpoint.CoveredMessages != len(request.Messages)-1 {
		t.Fatalf("checkpoint=%+v", checkpoint)
	}
	countedRequest := cloneRequest(request)
	countedRequest.Messages = result.Messages
	count, err := (&canonicalTokenCounter{}).CountInput(context.Background(), usage.CountRequest{Invocation: countedRequest})
	if err != nil || count.InputTokens > 1500 {
		t.Fatalf("result tokens=%d error=%v", count.InputTokens, err)
	}
	if !reflect.DeepEqual(request, original) {
		t.Fatal("raw invocation was modified")
	}
}

func TestSoftRecentCannotExpandIntoIncompleteToolRound(t *testing.T) {
	t.Parallel()
	request := shortHistoryWithLargeRecentRound()
	request.Messages[4].ToolCalls = append(request.Messages[4].ToolCalls, tool.Call{
		ID: "pending", Name: "read_next_log", Arguments: json.RawMessage(`{}`),
	})
	original := cloneRequest(request)
	models := &fakeModel{responses: []model.Response{
		summaryResponse(`{"summary":"` + strings.Repeat("Old greeting detail. ", 100) + `","operations":[]}`),
	}}
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	compactor := newTokenTestCompactor(t, Config{
		TriggerInputTokens: 3000, TargetInputTokens: 1500, RecentRounds: 1,
	}, &canonicalTokenCounter{}, models, store)
	_, err := compactor.Compact(context.Background(), contextwindow.CompactionRequest{SessionID: "s", Invocation: request})
	if !errors.Is(err, ErrContextUncompactable) || len(models.requests) != 1 || len(store.entries["s"]) != 0 {
		t.Fatalf("error=%v calls=%d entries=%d", err, len(models.requests), len(store.entries["s"]))
	}
	var input segmentInput
	if err := json.Unmarshal([]byte(messageText(models.requests[0].Messages[1])), &input); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(input.Source, projectMessages(request.Messages[1:3])) {
		t.Fatal("summary included part of the incomplete tool round")
	}
	if !reflect.DeepEqual(request, original) {
		t.Fatal("open suffix was modified")
	}
}

func TestSoftRecentExpansionSharesSummaryCallBudget(t *testing.T) {
	t.Parallel()
	request := shortHistoryWithLargeRecentRound()
	models := &fakeModel{responses: []model.Response{
		summaryResponse(`{"summary":"` + strings.Repeat("Old greeting detail. ", 100) + `","operations":[]}`),
	}}
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	compactor := newTokenTestCompactor(t, Config{
		TriggerInputTokens: 3000, TargetInputTokens: 1500, RecentRounds: 1, MaxSummaryPasses: 1,
	}, &canonicalTokenCounter{}, models, store)
	_, err := compactor.Compact(context.Background(), contextwindow.CompactionRequest{SessionID: "s", Invocation: request})
	if !errors.Is(err, ErrContextUncompactable) || len(models.requests) != 1 || len(store.entries["s"]) != 0 {
		t.Fatalf("error=%v calls=%d entries=%d", err, len(models.requests), len(store.entries["s"]))
	}
}

func shortHistoryWithLargeRecentRound() model.Request {
	request := firstToolRound()
	messages := cloneMessages(request.Messages[:1])
	messages = append(messages,
		model.Message{Role: model.RoleUser, Content: textContent("hi")},
		model.Message{Role: model.RoleAssistant, Content: textContent("ok")},
	)
	request.Messages = append(messages, cloneMessages(request.Messages[1:])...)
	return request
}
