package agentdefault

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/tool"
)

func TestStreamRetryKeepsEarlierRoundsAndExecutesToolsOnce(t *testing.T) {
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	models := &sequenceModel{}
	tools := &fakeTools{}
	invocations := 0
	streaming := modelStreamFunc(func(_ context.Context, _ model.Request, h model.StreamHandler) (model.Response, error) {
		invocations++
		if invocations == 1 {
			if err := h(model.StreamEvent{Kind: model.StreamPartDelta, TextDelta: "已完成🙂"}); err != nil {
				return model.Response{}, err
			}
			return model.Response{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("已完成🙂"), ToolCalls: []tool.Call{{ID: "once", Name: "echo", Arguments: json.RawMessage(`{}`)}}}}, nil
		}
		for _, e := range []model.StreamEvent{
			{Kind: model.StreamPartDelta, TextDelta: "失败🙂"},
			{Kind: model.StreamPartDelta, Semantic: model.StreamSemanticReasoning, TextDelta: "思考"},
			{Kind: model.StreamRetry, Attempt: 2, RetryDelay: time.Second},
			{Kind: model.StreamPartDelta, TextDelta: "又失败"},
			{Kind: model.StreamRetry, Attempt: 3, RetryDelay: 2 * time.Second},
			{Kind: model.StreamPartDelta, TextDelta: "成功"},
		} {
			if err := h(e); err != nil {
				return model.Response{}, err
			}
		}
		return model.Response{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("成功")}}, nil
	})
	exports, _, err := New(context.Background(), withState(t, Config{}, Dependencies{Model: models, Streaming: ingotabi.Some[model.StreamingRuntime](streaming), Tools: tools, Store: store, Assets: newMemoryAssets(), Prompt: passthroughPrompt{}}))
	if err != nil {
		t.Fatal(err)
	}
	output, reasoning := "", ""
	resets, recovered := 0, 0
	result, err := exports.Runtime.(*runtime).stream(context.Background(), agent.Turn{SessionID: "s", Inputs: []agent.Input{{Text: "go"}}}, func(e agent.StreamEvent) error {
		switch e.Kind {
		case agent.StreamOutputDelta:
			output += e.TextDelta
		case agent.StreamReasoningDelta:
			reasoning += e.TextDelta
		case agent.StreamRetry:
			resets++
			if e.Attempt != resets+1 {
				t.Fatalf("retry=%+v", e)
			}
			output = output[:len(output)-e.DiscardOutputBytes]
			reasoning = reasoning[:len(reasoning)-e.DiscardReasoningBytes]
			if output != "已完成🙂" || reasoning != "" {
				t.Fatalf("prior round lost: %q / %q", output, reasoning)
			}
		case agent.StreamRecovered:
			recovered++
		}
		return nil
	})
	if err != nil || result.Result == nil || output != "已完成🙂成功" || resets != 2 || recovered != 1 || invocations != 2 || len(tools.calls) != 1 {
		t.Fatalf("result=%+v output=%q resets=%d recovered=%d invocations=%d tools=%v err=%v", result, output, resets, recovered, invocations, tools.calls, err)
	}
	history, err := exports.History.Load(context.Background(), "s")
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range history {
		for _, part := range msg.Content {
			if part.Text == "失败🙂" || part.Text == "又失败" || part.Text == "思考" {
				t.Fatalf("failed attempt persisted: %+v", msg)
			}
		}
	}
}

func TestStreamRetryConsumerFailureStopsTools(t *testing.T) {
	for _, failKind := range []agent.StreamEventKind{agent.StreamRetry, agent.StreamRecovered} {
		store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
		tools := &fakeTools{}
		streaming := modelStreamFunc(func(_ context.Context, _ model.Request, h model.StreamHandler) (model.Response, error) {
			if err := h(model.StreamEvent{Kind: model.StreamRetry, Attempt: 2}); err != nil {
				return model.Response{}, err
			}
			return model.Response{Message: model.Message{Role: model.RoleAssistant, ToolCalls: []tool.Call{{ID: "must-not-run", Name: "echo", Arguments: json.RawMessage(`{}`)}}}}, nil
		})
		exports, _, err := New(context.Background(), withState(t, Config{}, Dependencies{Model: &sequenceModel{}, Streaming: ingotabi.Some[model.StreamingRuntime](streaming), Tools: tools, Store: store, Assets: newMemoryAssets(), Prompt: passthroughPrompt{}}))
		if err != nil {
			t.Fatal(err)
		}
		consumer := errors.New("consumer unavailable")
		_, err = exports.Runtime.(*runtime).stream(context.Background(), agent.Turn{SessionID: "s", Inputs: []agent.Input{{Text: "go"}}}, func(e agent.StreamEvent) error {
			if e.Kind == failKind {
				return consumer
			}
			return nil
		})
		if !errors.Is(err, consumer) || len(tools.calls) != 0 {
			t.Fatalf("tools=%v err=%v", tools.calls, err)
		}
	}
}
