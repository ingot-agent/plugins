package hostcomponent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	appbackend "github.com/ingot-agent/plugins/app-webui"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/observation"
	"github.com/ingot-agent/sdk/tool"
)

func testProgressObserver(t *testing.T) (*observer, *eventHub) {
	t.Helper()
	hub := newEventHub(1024, 4)
	o := &observer{events: hub, pending: make(map[observation.Correlation]*observationBatch)}
	t.Cleanup(func() { _ = o.cleanup(context.Background()) })
	return o, hub
}

func TestDiagnosticBurstDoesNotEvictSubscriber(t *testing.T) {
	o, hub := testProgressObserver(t)
	sub, err := hub.Subscribe(0)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	scope := observation.Correlation{SessionID: "s", TurnID: "turn", RoundIndex: 1}
	o.Observe(observation.Event{Correlation: scope, Detail: observation.ModelProgress{Progress: model.StreamEvent{Kind: model.StreamPartStart, Semantic: model.StreamSemanticContent, PartKind: content.KindText}}})
	for i := range 500 {
		o.Observe(observation.Event{Sequence: uint64(i + 1), Correlation: scope, Detail: observation.ModelProgress{Progress: model.StreamEvent{Kind: model.StreamPartDelta, Semantic: model.StreamSemanticContent, TextDelta: "字"}}})
	}
	o.Observe(observation.Event{Sequence: 501, Correlation: scope, Detail: observation.ModelFinished{Status: observation.StatusSucceeded}})
	if hub.Cursor() != 3 {
		t.Fatalf("500 diagnostic chunks produced %d events instead of start/batch/finish", hub.Cursor())
	}
	var records []appbackend.EventRecord
	for range 3 {
		select {
		case record, ok := <-sub.Events():
			if !ok {
				t.Fatal("subscriber evicted by diagnostic burst")
			}
			records = append(records, record)
		case <-time.After(time.Second):
			t.Fatal("missing diagnostic boundary")
		}
	}
	var batch struct {
		Data struct {
			Sequence uint64
			Progress struct{ TextDelta string }
		}
	}
	if err := json.Unmarshal(records[1].Data, &batch); err != nil {
		t.Fatal(err)
	}
	if batch.Data.Sequence != 500 || batch.Data.Progress.TextDelta != strings.Repeat("字", 500) || !strings.Contains(string(records[2].Data), "agent.model.finished") {
		t.Fatal("diagnostic batching lost text, sequence or order")
	}
}

func TestDiagnosticTimerAndCleanupFlushPendingProgress(t *testing.T) {
	o, hub := testProgressObserver(t)
	sub, _ := hub.Subscribe(0)
	defer sub.Close()
	scope := observation.Correlation{SessionID: "s", TurnID: "turn", ToolCallID: "tool"}
	detail := &observation.ToolProgress{Progress: tool.Progress{Channel: "log", Content: content.FromText("working")}}
	o.Observe(observation.Event{Correlation: scope, Detail: detail})
	detail.Progress.Content[0].Text = "caller mutation"
	select {
	case record := <-sub.Events():
		if !strings.Contains(string(record.Data), "working") || strings.Contains(string(record.Data), "caller mutation") {
			t.Fatal("pending diagnostic data was not detached")
		}
	case <-time.After(time.Second):
		t.Fatal("blocked tool progress did not flush on its timer")
	}
	o.Observe(observation.Event{Correlation: scope, Detail: observation.ToolProgress{Progress: tool.Progress{Content: content.FromText("tail")}}})
	_ = o.cleanup(context.Background())
	if hub.Cursor() != 2 {
		t.Fatal("cleanup dropped the pending tail")
	}
	o.Observe(observation.Event{Correlation: scope, Detail: detail})
	if hub.Cursor() != 2 {
		t.Fatal("closed observer accepted new progress")
	}
}

func TestDiagnosticBatchesKeepPartAndSessionBoundaries(t *testing.T) {
	o, hub := testProgressObserver(t)
	for _, semantic := range []model.StreamSemantic{model.StreamSemanticReasoning, model.StreamSemanticContent, model.StreamSemanticReasoning} {
		o.Observe(observation.Event{Correlation: observation.Correlation{SessionID: "a", TurnID: "turn"}, Detail: observation.ModelProgress{Progress: model.StreamEvent{Kind: model.StreamPartDelta, Semantic: semantic, TextDelta: "a"}}})
	}
	o.Observe(observation.Event{Correlation: observation.Correlation{SessionID: "b", TurnID: "turn"}, Detail: observation.ModelProgress{Progress: model.StreamEvent{Kind: model.StreamPartDelta, Semantic: model.StreamSemanticContent, TextDelta: "b"}}})
	o.Observe(observation.Event{Correlation: observation.Correlation{SessionID: "a", TurnID: "turn"}, Detail: observation.RoundFinished{}})
	if hub.Cursor() != 4 {
		t.Fatal("round boundary failed to flush ordered parts independently")
	}
	_ = o.cleanup(context.Background())
	sub, _ := hub.Subscribe(0)
	defer sub.Close()
	records := sub.Replay()
	if len(records) != 5 || !strings.Contains(string(records[0].Data), `"semantic":"reasoning"`) || !strings.Contains(string(records[1].Data), `"semantic":"content"`) || !strings.Contains(string(records[2].Data), `"semantic":"reasoning"`) || !strings.Contains(string(records[4].Data), `"sessionId":"b"`) {
		t.Fatal("interleaved parts or sessions were merged")
	}
}
