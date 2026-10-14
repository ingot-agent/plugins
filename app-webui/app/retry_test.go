package appcomponent

import (
	"github.com/ingot-agent/sdk/agent"
	"testing"
)

func TestRetryProjectionPreservesEarlierRoundsAndOwnsStatus(t *testing.T) {
	state := agent.RuntimeState{Revision: 5,
		Messages: []agent.MessageState{{ID: "receipt", Request: agent.MessageRequest{SessionID: "s"}, TurnID: "turn", Status: "running", Persisted: true}},
		Turns: []agent.TurnState{{TurnID: "turn", Status: "running", Blocks: []agent.ExecutionBlock{
			{Kind: "input", InputIndex: 0},
			{Kind: "round", RoundIndex: 0, Output: "上一轮🙂", Reasoning: "之前的思考"},
			{Kind: "round", RoundIndex: 1, Retry: &agent.RetryState{Attempt: 2, DelayMS: 1000}},
		}}},
	}
	snap := projectSubmissions(state)[0]
	if snap.Output != "上一轮🙂" || snap.Reasoning != "之前的思考" || snap.Retry == nil || snap.Retry.Attempt != 2 || snap.Retry.DelayMS != 1000 {
		t.Fatalf("snapshot=%+v", snap)
	}
	snap.Retry.Attempt = 999
	if projectSubmissions(state)[0].Retry.Attempt != 2 {
		t.Fatal("projection aliases retry state")
	}
	state.Turns[0].Status = "canceled"
	if projectSubmissions(state)[0].Retry != nil {
		t.Fatal("retry status survived cancellation")
	}
	state.Turns[0].Status = "running"
	state.Turns[0].Blocks[2].Retry = nil
	state.Turns[0].Blocks[2].Output = "成功"
	if snap = projectSubmissions(state)[0]; snap.Retry != nil || snap.Output != "上一轮🙂成功" {
		t.Fatalf("recovered snapshot=%+v", snap)
	}
}
