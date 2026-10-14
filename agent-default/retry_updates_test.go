package agentdefault

import (
	"context"
	"testing"
	"time"

	"github.com/ingot-agent/sdk/agent"
)

func TestRetryUpdatesRetractPendingAndPublishedTextWithoutLosingPriorRounds(t *testing.T) {
	r, run := newUpdateRuntime(t)
	emit := func(event agent.StreamEvent) {
		t.Helper()
		if err := r.streamProgress(run, event); err != nil {
			t.Fatal(err)
		}
	}
	emit(agent.StreamEvent{Kind: agent.StreamOutputDelta, TextDelta: "上一轮🙂"})
	r.roundStarted(run, 1)
	emit(agent.StreamEvent{Kind: agent.StreamOutputDelta, TextDelta: "失败🙂"})
	before, _ := r.State(context.Background())                                 // Some text was already published.
	emit(agent.StreamEvent{Kind: agent.StreamReasoningDelta, TextDelta: "思考"}) // Still pending.
	emit(agent.StreamEvent{Kind: agent.StreamRetry, DiscardOutputBytes: len("失败🙂"), DiscardReasoningBytes: len("思考"), Attempt: 2, RetryDelay: time.Second})
	state, _ := r.State(context.Background())
	blocks := state.Turns[0].Blocks
	last := blocks[len(blocks)-1]
	if blocks[len(blocks)-2].Output != "上一轮🙂" || last.Output != "" || last.Reasoning != "" || last.Retry == nil || last.Retry.Attempt != 2 {
		t.Fatalf("blocks=%+v", blocks)
	}
	last.Retry.Attempt = 999
	owned, _ := r.State(context.Background())
	if owned.Turns[0].Blocks[len(blocks)-1].Retry.Attempt != 2 {
		t.Fatal("snapshot aliases retry")
	}
	// The watch journal must flush pending progress before its replacement.
	u := watchUpdate(t, r, before.Revision)
	if u.Progress == nil || u.Progress.ReasoningDelta != "思考" {
		t.Fatalf("pending=%+v", u)
	}
	u = watchUpdate(t, r, u.Revision)
	if len(u.Turns) != 1 || u.Turns[0].Blocks[len(blocks)-1].Retry == nil {
		t.Fatalf("reset=%+v", u)
	}
	emit(agent.StreamEvent{Kind: agent.StreamOutputDelta, TextDelta: "成功"})
	state, _ = r.State(context.Background())
	last = state.Turns[0].Blocks[len(blocks)-1]
	if last.Output != "成功" || last.Retry != nil {
		t.Fatalf("recovered=%+v", last)
	}
	for _, count := range []int{-1, 1, 999} {
		if err := r.streamProgress(run, agent.StreamEvent{Kind: agent.StreamRetry, Attempt: 3, DiscardOutputBytes: count}); err == nil {
			t.Fatalf("accepted invalid retraction %d", count)
		}
	}
	after, _ := r.State(context.Background())
	if after.Revision != state.Revision {
		t.Fatal("invalid retraction mutated state")
	}
	emit(agent.StreamEvent{Kind: agent.StreamRetry, Attempt: 3, DiscardOutputBytes: len("成功")})
	emit(agent.StreamEvent{Kind: agent.StreamRecovered})
	after, _ = r.State(context.Background())
	if after.Turns[0].Blocks[len(blocks)-1].Retry != nil {
		t.Fatal("tool-only recovery retained retry status")
	}
}
