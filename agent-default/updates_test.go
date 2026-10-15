package agentdefault

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/observation"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/tool"
)

func newUpdateRuntime(t testing.TB) (*runtime, *activeExecution) {
	t.Helper()
	r := &runtime{}
	r.initMessages()
	queue := &messageQueue{id: "s", root: "s"}
	first := &queuedMessage{state: agent.MessageState{ID: "receipt", Request: agent.MessageRequest{SessionID: "s", Input: agent.Input{Text: "first"}}, Sequence: 1, Status: "running", Started: true}}
	run := &activeExecution{queue: queue, first: first, items: []*queuedMessage{first}, cancel: func() {}}
	run.state.Status = "running"
	queue.active = run
	r.queues[queue.id], r.submissions[first.state.ID] = queue, first
	r.beginMessageTurn(run, "turn", agent.Turn{SessionID: "s", Inputs: []agent.Input{first.state.Request.Input}})
	r.inputPersisted(run, preparedInput{submission: first})
	r.roundStarted(run, 0)
	t.Cleanup(func() {
		r.queueMu.Lock()
		defer r.queueMu.Unlock()
		r.flushAllProgressLocked()
		r.runCancel()
	})
	return r, run
}

func watchUpdate(t *testing.T, r *runtime, after uint64) agent.RuntimeUpdate {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	update, err := r.Watch(ctx, after)
	if err != nil {
		t.Fatal(err)
	}
	return update
}

func retainLargeTurns(r *runtime, count int) {
	for i := range count {
		id := fmt.Sprint("old-", i)
		r.completedTurns[id] = agent.TurnState{TurnID: id, Turn: agent.Turn{SessionID: session.ID(id)}, Blocks: []agent.ExecutionBlock{{Kind: "round", Messages: []model.Message{{Role: model.RoleAssistant, Content: content.Content{content.Inline(content.KindImage, "image/png", "image.png", make([]byte, 64<<10))}}}}}}
		r.turnOrder = append(r.turnOrder, id)
	}
}

func TestProgressBatchesDoNotCarryRetainedState(t *testing.T) {
	r, run := newUpdateRuntime(t)
	retainLargeTurns(r, messageRetention)
	after := r.revision
	// Burst of 500 small chunks: the payload is just new text, regardless of
	// the 8 MiB of completed inline media retained by other sessions.
	for range 500 {
		r.streamProgress(run, agent.StreamEvent{Kind: agent.StreamOutputDelta, TextDelta: "字"})
	}
	r.roundFinished(run, agent.RoundResult{Decision: model.Message{Role: model.RoleAssistant, Content: content.FromText(strings.Repeat("字", 500))}})
	var output strings.Builder
	batches := 0
	for after < r.revision {
		update := watchUpdate(t, r, after)
		after = update.Revision
		if update.Progress != nil {
			batches++
			output.WriteString(update.Progress.OutputDelta)
			if len(update.Turns)+len(update.Messages)+len(update.Queues) != 0 || update.Reset {
				t.Fatal("progress copied runtime state")
			}
		} else if len(update.Turns) != 1 || update.Turns[0].TurnID != "turn" || len(update.Messages) != 1 {
			t.Fatal("structural update included unrelated retained turns")
		}
	}
	if output.String() != strings.Repeat("字", 500) || batches >= 50 {
		t.Fatalf("lost text or failed coalescing: bytes=%d batches=%d", output.Len(), batches)
	}
}

func TestProgressFlushesByTimeAndSizeWithoutACompletedRound(t *testing.T) {
	r, run := newUpdateRuntime(t)
	after := r.revision
	r.streamProgress(run, agent.StreamEvent{Kind: agent.StreamReasoningDelta, TextDelta: "thinking"})
	timed := watchUpdate(t, r, after)
	if timed.Progress == nil || timed.Progress.ReasoningDelta != "thinking" {
		t.Fatalf("silent/blocked model lost timed progress: %+v", timed)
	}
	r.streamProgress(run, agent.StreamEvent{Kind: agent.StreamOutputDelta, TextDelta: strings.Repeat("x", progressBatchBytes)})
	r.queueMu.Lock()
	flushed := r.revision > timed.Revision && run.progressTimer == nil
	r.queueMu.Unlock()
	if !flushed {
		t.Fatal("size threshold waited for the timer")
	}
	update := watchUpdate(t, r, timed.Revision)
	if update.Progress == nil || len(update.Progress.OutputDelta) != progressBatchBytes {
		t.Fatal("size flush lost output")
	}
}

func TestProgressFlushesBeforeInputAndRoundBoundaries(t *testing.T) {
	r, run := newUpdateRuntime(t)
	after := r.revision
	r.streamProgress(run, agent.StreamEvent{Kind: agent.StreamOutputDelta, TextDelta: "first"})
	r.roundFinished(run, agent.RoundResult{Decision: model.Message{Role: model.RoleAssistant, Content: content.FromText("canonical first")}})
	first := watchUpdate(t, r, after)
	boundary := watchUpdate(t, r, first.Revision)
	if first.Progress == nil || first.Progress.MessageID != "receipt" || first.Progress.RoundIndex != 0 || first.Progress.OutputDelta != "first" || boundary.Progress != nil || len(boundary.Turns[0].Blocks[1].Messages) != 1 {
		t.Fatal("round state overtook its pending text")
	}
	second := &queuedMessage{state: agent.MessageState{ID: "receipt-2", Request: agent.MessageRequest{SessionID: "s", Input: agent.Input{Text: "second"}}, Sequence: 2, Status: "running", TurnID: "turn"}}
	run.items = append(run.items, second)
	r.submissions[second.state.ID] = second
	r.inputPersisted(run, preparedInput{submission: second, inputIndex: 1})
	r.roundStarted(run, 1)
	after = r.revision
	r.streamProgress(run, agent.StreamEvent{Kind: agent.StreamOutputDelta, TextDelta: "second"})
	state, _ := r.State(context.Background()) // Also flushes and covers this batch.
	update := watchUpdate(t, r, after)
	if update.Progress == nil || update.Progress.MessageID != "receipt-2" || update.Progress.RoundIndex != 1 || update.Progress.OutputDelta != "second" || state.Revision != update.Revision {
		t.Fatalf("input/round association or recovery revision: %+v", update)
	}
}

func TestToolProgressAndCancellationFlushInOrder(t *testing.T) {
	r, run := newUpdateRuntime(t)
	ctx := observation.WithCorrelation(context.Background(), observation.Correlation{ToolCallID: "call"})
	r.toolProgress(run, ctx, observation.ToolStarted{Call: tool.Call{ID: "call", Name: "tool"}})
	after := r.revision
	r.toolProgress(run, ctx, observation.ToolProgress{Progress: tool.Progress{Content: content.FromText("working")}})
	r.streamProgress(run, agent.StreamEvent{Kind: agent.StreamOutputDelta, TextDelta: "partial"})
	// Pausing/stopping is a structural boundary and must not drop the tail.
	if err := r.Stop(context.Background(), "s"); err != nil {
		t.Fatal(err)
	}
	progress := watchUpdate(t, r, after)
	stopped := watchUpdate(t, r, progress.Revision)
	if progress.Progress == nil || progress.Progress.OutputDelta != "partial" || len(progress.Progress.Tools) != 1 || progress.Progress.Tools[0].Content[0].Text != "working" || stopped.Progress != nil || !stopped.Queues[0].Paused {
		t.Fatal("stop lost or reordered pending progress")
	}
	r.toolProgress(run, ctx, observation.ToolFinished{Status: observation.StatusCanceled, Result: &tool.Result{Content: content.FromText("final")}})
	finished := watchUpdate(t, r, stopped.Revision)
	if finished.Turns[0].Blocks[1].Tools[0].Content[0].Text != "final" {
		t.Fatal("tool result was not authoritative")
	}
}

func TestSlowAndIndependentWatchersRecoverWithoutBlockingExecution(t *testing.T) {
	r, run := newUpdateRuntime(t)
	after := r.revision
	for range runtimeUpdateRetention + 2 {
		r.streamProgress(run, agent.StreamEvent{Kind: agent.StreamOutputDelta, TextDelta: strings.Repeat("x", progressBatchBytes)})
	}
	reset := watchUpdate(t, r, after)
	if !reset.Reset || reset.Progress != nil || len(reset.Turns[0].Blocks[1].Output) != (runtimeUpdateRetention+2)*progressBatchBytes {
		t.Fatal("lagging watcher did not recover the full answer")
	}
	r.streamProgress(run, agent.StreamEvent{Kind: agent.StreamOutputDelta, TextDelta: "tail"})
	first := watchUpdate(t, r, reset.Revision)
	first.Progress.OutputDelta = "caller mutation"
	second := watchUpdate(t, r, reset.Revision)
	if second.Progress.OutputDelta != "tail" || second.Revision != first.Revision {
		t.Fatal("watcher consumed or modified another watcher's batch")
	}
}

func TestRepeatedStopAfterSettlementDoesNotPublishAPartialLifecycle(t *testing.T) {
	r, run := newUpdateRuntime(t)
	second := &queuedMessage{state: agent.MessageState{ID: "receipt-2", Request: agent.MessageRequest{SessionID: "s"}, Status: "canceled", Started: true, Persisted: true, TurnID: "turn", Sequence: 2}}
	run.first.state.Status = "canceled"
	r.submissions[second.state.ID] = second
	r.completedTurns["turn"] = captureTurnState(run.state)
	run.queue.active = nil
	after := r.revision
	if err := r.CancelMessage(context.Background(), second.state.ID, false); err != nil {
		t.Fatal(err)
	}
	if r.revision != after {
		t.Fatal("repeated stop published a subset of the settled Turn's receipts")
	}
}

func TestCapturedStateIsStableAndDetachedDuringConcurrentProgress(t *testing.T) {
	r, run := newUpdateRuntime(t)
	ctx := observation.WithCorrelation(context.Background(), observation.Correlation{ToolCallID: "call"})
	r.toolProgress(run, ctx, observation.ToolStarted{Call: tool.Call{ID: "call", Name: "tool", Arguments: []byte(`{"x":1}`)}})
	r.toolProgress(run, ctx, observation.ToolProgress{Progress: tool.Progress{Content: content.Content{content.Inline(content.KindImage, "image/png", "", []byte{1})}}})
	baseline, _ := r.State(context.Background())
	baseline.Turns[0].Blocks[1].Tools[0].Content[0].Media.Source.Data[0] = 9
	baseline.Turns[0].Blocks[1].Tools[0].Call.Arguments[0] = 'x'
	baseline.Messages[0].Request.Input.Text = "caller"
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for range 200 {
			r.streamProgress(run, agent.StreamEvent{Kind: agent.StreamOutputDelta, TextDelta: "a"})
			r.toolProgress(run, ctx, observation.ToolProgress{Progress: tool.Progress{Content: content.FromText("working")}})
		}
	}()
	for range 20 {
		state, _ := r.State(context.Background())
		if state.Messages[0].Request.Input.Text != "first" || state.Turns[0].Blocks[1].Tools[0].Content[0].Media.Source.Data[0] != 1 || state.Turns[0].Blocks[1].Tools[0].Call.Arguments[0] != '{' {
			t.Fatal("caller mutation reached live state")
		}
	}
	workers.Wait()
	state, _ := r.State(context.Background())
	if state.Turns[0].Blocks[1].Output != strings.Repeat("a", 200) || baseline.Turns[0].Blocks[1].Output != "" {
		t.Fatal("captured snapshot changed or progress was lost")
	}
}

func BenchmarkRuntimeProgress(b *testing.B) {
	for _, retained := range []int{0, messageRetention} {
		b.Run(fmt.Sprintf("retained-%d", retained), func(b *testing.B) {
			r, run := newUpdateRuntime(b)
			retainLargeTurns(r, retained)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				r.queueMu.Lock()
				run.state.Blocks = nil
				r.queueMu.Unlock()
				r.roundStarted(run, 0)
				after := r.revision
				for range 500 {
					r.streamProgress(run, agent.StreamEvent{Kind: agent.StreamOutputDelta, TextDelta: "x"})
				}
				r.queueMu.Lock()
				r.flushProgressLocked(run)
				r.queueMu.Unlock()
				if _, err := r.Watch(context.Background(), after); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
