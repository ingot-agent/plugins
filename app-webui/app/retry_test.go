package appcomponent

import (
	"context"
	"testing"
	"time"

	"github.com/ingot-agent/sdk/agent"
)

func TestRetryProjectionRetractsOnlyAttemptAndSnapshotsStatus(t *testing.T) {
	a := testApplication(t)
	invocation := &turnInvocation{id: "retry", sessionID: "s"}
	a.turns.running[invocation.id] = invocation
	t.Cleanup(func() { delete(a.turns.running, invocation.id) })
	ctx := context.Background()
	for _, event := range []agent.StreamEvent{
		{Kind: agent.StreamOutputDelta, TextDelta: "上一轮🙂"},
		{Kind: agent.StreamReasoningDelta, TextDelta: "之前的思考"},
		{Kind: agent.StreamOutputDelta, TextDelta: "失败内容🌍"},
		{Kind: agent.StreamReasoningDelta, TextDelta: "失败思考"},
		{Kind: agent.StreamRetry, DiscardOutputBytes: len("失败内容🌍"), DiscardReasoningBytes: len("失败思考"), Attempt: 2, RetryDelay: time.Second},
	} {
		if err := a.turns.delta(ctx, invocation, event); err != nil {
			t.Fatal(err)
		}
	}
	snap := a.turns.Snapshots()[0]
	if snap.Output != "上一轮🙂" || snap.Reasoning != "之前的思考" || snap.Revision != 5 || snap.Retry == nil || snap.Retry.Attempt != 2 || snap.Retry.DelayMS != 1000 {
		t.Fatalf("snapshot=%+v", snap)
	}
	snap.Retry.Attempt = 999
	if a.turns.Snapshots()[0].Retry.Attempt != 2 {
		t.Fatal("snapshot aliases mutable retry state")
	}
	if err := a.turns.delta(ctx, invocation, agent.StreamEvent{Kind: agent.StreamRetry, Attempt: 3, DiscardOutputBytes: 1}); err == nil {
		t.Fatal("accepted truncated UTF-8")
	}
	if err := a.turns.delta(ctx, invocation, agent.StreamEvent{Kind: agent.StreamRetry, Attempt: 3, DiscardReasoningBytes: 999}); err == nil {
		t.Fatal("accepted excessive retraction")
	}
	if invocation.revision != 5 {
		t.Fatal("invalid events mutated state")
	}
	if err := a.turns.delta(ctx, invocation, agent.StreamEvent{Kind: agent.StreamRecovered}); err != nil {
		t.Fatal(err)
	}
	if a.turns.Snapshots()[0].Retry != nil {
		t.Fatal("retry status survived successful tool-only response")
	}
	if err := a.turns.delta(ctx, invocation, agent.StreamEvent{Kind: agent.StreamOutputDelta, TextDelta: "成功"}); err != nil {
		t.Fatal(err)
	}
	if invocation.output != "上一轮🙂成功" {
		t.Fatalf("output=%q", invocation.output)
	}
}
