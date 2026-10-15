package agentdefault

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/tool"
)

func TestHistoryReplaySurvivesToolRoundAndRestart(t *testing.T) {
	arguments := json.RawMessage(" { \"cmd\": \"a && b > out\",\n \"value\": 1 } ")
	replay := &model.ReplayState{Format: "example.output.v1", Provider: "p", Model: "m", Data: []byte{0, 255, 1, '<', '&', '>'}}
	decision := model.Message{Role: model.RoleAssistant, Content: content.FromText("working"), ToolCalls: []tool.Call{{ID: "c1", Name: "echo", Arguments: arguments}}, Replay: replay}
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	firstModels := &sequenceModel{responses: []model.Response{{Message: decision}, {Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("done")}}}}
	exports, cleanup, err := New(context.Background(), withState(t, Config{}, Dependencies{Model: firstModels, Tools: &fakeTools{}, Store: store, Assets: newMemoryAssets(), Prompt: passthroughPrompt{}}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exports.Runtime.(*runtime).execute(context.Background(), agent.Turn{SessionID: "s", Inputs: []agent.Input{{Text: "hello"}}}, nil); err != nil {
		t.Fatal(err)
	}
	if cleanup != nil {
		if err := cleanup(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	sameTurn := firstModels.requests[1].Messages[1]
	if !reflect.DeepEqual(sameTurn, decision) {
		t.Fatal("same-turn history lost provider state or argument bytes")
	}
	if store.entries["s"][1].Version != 2 {
		t.Fatal("new message must use v2 persistence")
	}
	// Exercise a new runtime reading durable history, not just the payload codec.
	secondModels := &sequenceModel{responses: []model.Response{{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("continued")}}}}
	restarted, cleanup, err := New(context.Background(), withState(t, Config{}, Dependencies{Model: secondModels, Tools: &fakeTools{}, Store: store, Assets: newMemoryAssets(), Prompt: passthroughPrompt{}}))
	if err != nil {
		t.Fatal(err)
	}
	if cleanup != nil {
		defer cleanup(context.Background())
	}
	if _, err := restarted.Runtime.(*runtime).execute(context.Background(), agent.Turn{SessionID: "s", Inputs: []agent.Input{{Text: "continue"}}}, nil); err != nil {
		t.Fatal(err)
	}
	restored := secondModels.requests[0].Messages[1]
	if !reflect.DeepEqual(restored, decision) {
		t.Fatal("restart changed previous assistant message")
	}
	restored.Replay.Data[0] = 9
	restored.ToolCalls[0].Arguments[0] = 'X'
	if !bytes.Equal(sameTurn.Replay.Data, replay.Data) || !bytes.Equal(sameTurn.ToolCalls[0].Arguments, arguments) {
		t.Fatal("history aliases previous request")
	}
}

func TestHistoryReadsLegacyV1Arguments(t *testing.T) {
	// A literal pre-upgrade record is essential: the current encoder writes v2.
	raw := []byte(`{"role":"assistant","content":[],"name":"","tool_call_id":"","tool_calls":[{"id":"c1","name":"echo","arguments":{ "cmd": "a \u0026\u0026 b \u003e out" }}]}`)
	got, err := decodePersistedMessage(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Replay != nil || string(got.ToolCalls[0].Arguments) != `{ "cmd": "a \u0026\u0026 b \u003e out" }` {
		t.Fatal("legacy tool arguments changed")
	}
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {
		{Kind: agentMessageKind, Version: 1, Payload: []byte(`{"role":"user","content":[{"kind":"text","text":"hello"}],"name":"","tool_call_id":"","tool_calls":[]}`)},
		{Kind: agentMessageKind, Version: 1, Payload: raw},
	}}}
	runtime := &runtime{store: store}
	history, err := runtime.loadHistory(context.Background(), "s")
	if err != nil || len(history) != 2 {
		t.Fatalf("v1 load: messages=%d err=%v", len(history), err)
	}
}

func TestPersistedArgumentsRejectAmbiguousRepresentation(t *testing.T) {
	_, err := decodePersistedMessage([]byte(`{"role":"assistant","content":[],"tool_calls":[{"id":"c1","name":"echo","arguments":{},"argument_bytes":"e30="}]}`))
	if !errors.Is(err, ErrCorruptHistory) {
		t.Fatalf("ambiguous arguments: %v", err)
	}
}
