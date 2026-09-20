package agentdefault

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ingot-agent/plugins/agent-default/sessioncontrol"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/tool"
)

type subagentRuntimeControl struct {
	mu          sync.Mutex
	nextToken   uint64
	finish      sessioncontrol.FinishIntent
	finishOK    bool
	finishCalls []string
}

func (c *subagentRuntimeControl) BeginRoot(_ context.Context, id session.ID) (sessioncontrol.Handle, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextToken++
	return sessioncontrol.Handle{SessionID: id, Token: c.nextToken}, nil
}

func (*subagentRuntimeControl) EndRoot(context.Context, sessioncontrol.Handle, bool) error {
	return nil
}

func (*subagentRuntimeControl) Next(ctx context.Context) (sessioncontrol.Task, error) {
	<-ctx.Done()
	return sessioncontrol.Task{}, ctx.Err()
}

func (c *subagentRuntimeControl) FinishIntent(_ sessioncontrol.Handle, toolCallID string) (sessioncontrol.FinishIntent, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.finishCalls = append(c.finishCalls, toolCallID)
	return c.finish, c.finishOK, nil
}

func (*subagentRuntimeControl) Settle(context.Context, sessioncontrol.Handle, *sessioncontrol.FinishIntent, agent.Execution, error) error {
	return nil
}

func (*subagentRuntimeControl) ValidateTools([]tool.Definition) error { return nil }
func (*subagentRuntimeControl) Shutdown(context.Context) error        { return nil }

func (c *subagentRuntimeControl) finishCallSnapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.finishCalls...)
}

type subagentRuntimeTools struct {
	mu    sync.Mutex
	calls []tool.Invocation
}

func (*subagentRuntimeTools) Definitions() []tool.Definition {
	return []tool.Definition{
		{Name: "echo", Description: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: childSubmitToolName, Description: "submit", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
}

func (t *subagentRuntimeTools) Call(_ context.Context, invocation tool.Invocation) (tool.Result, error) {
	t.mu.Lock()
	t.calls = append(t.calls, cloneInvocation(invocation))
	t.mu.Unlock()
	return tool.Result{Content: content.FromText("tool-ok")}, nil
}

func (t *subagentRuntimeTools) callNames() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	result := make([]string, len(t.calls))
	for i, invocation := range t.calls {
		result[i] = invocation.Call.Name
	}
	return result
}

func TestChildLastRoundOnlyExposesSubmitAndAcceptsIt(t *testing.T) {
	store := &memoryStore{entries: map[session.ID][]session.Entry{"c1_child": {}}}
	models := &sequenceModel{responses: []model.Response{
		{Message: model.Message{Role: model.RoleAssistant, ToolCalls: []tool.Call{{ID: "echo-call", Name: "echo", Arguments: json.RawMessage(`{}`)}}}},
		{Message: model.Message{Role: model.RoleAssistant, ToolCalls: []tool.Call{{ID: "submit-call", Name: childSubmitToolName, Arguments: json.RawMessage(`{"result":"done"}`)}}}},
	}}
	tools := &subagentRuntimeTools{}
	control := &subagentRuntimeControl{
		finish:   sessioncontrol.FinishIntent{ToolCallID: "submit-call", Result: "done"},
		finishOK: true,
	}
	runtime := newSubagentTestRuntime(t, Config{MaxRounds: 2}, models, tools, store, control)
	frame := childTestFrame("c1_child", "echo", childSubmitToolName)

	execution, err := runtime.executeFrame(context.Background(), agent.Turn{SessionID: "c1_child", Input: "task"}, nil, frame)
	if err != nil {
		t.Fatal(err)
	}
	if textValue(executionOutput(execution)) != "done" || frame.confirmed == nil || frame.confirmed.Result != "done" {
		t.Fatalf("execution=%#v confirmed=%#v", execution, frame.confirmed)
	}
	if got := tools.callNames(); !reflect.DeepEqual(got, []string{"echo", childSubmitToolName}) {
		t.Fatalf("tool calls=%v", got)
	}
	if got := control.finishCallSnapshot(); !reflect.DeepEqual(got, []string{"submit-call"}) {
		t.Fatalf("finish calls=%v", got)
	}
	models.mu.Lock()
	requests := append([]model.Request(nil), models.requests...)
	models.mu.Unlock()
	if len(requests) != 2 || !reflect.DeepEqual(definitionNames(requests[1].Tools), []string{childSubmitToolName}) {
		t.Fatalf("model requests=%#v", requests)
	}
}

func TestChildRejectsMixedSubmissionBeforeToolDispatch(t *testing.T) {
	store := &memoryStore{entries: map[session.ID][]session.Entry{"c1_child": {}}}
	models := &sequenceModel{responses: []model.Response{{Message: model.Message{
		Role: model.RoleAssistant,
		ToolCalls: []tool.Call{
			{ID: "submit-call", Name: childSubmitToolName, Arguments: json.RawMessage(`{"result":"done"}`)},
			{ID: "echo-call", Name: "echo", Arguments: json.RawMessage(`{}`)},
		},
	}}}}
	tools := &subagentRuntimeTools{}
	control := &subagentRuntimeControl{}
	runtime := newSubagentTestRuntime(t, Config{MaxRounds: 2}, models, tools, store, control)

	_, err := runtime.executeFrame(context.Background(), agent.Turn{SessionID: "c1_child", Input: "task"}, nil,
		childTestFrame("c1_child", "echo", childSubmitToolName))
	if !errors.Is(err, ErrInvalidSubmissionRound) {
		t.Fatalf("error=%v", err)
	}
	if got := tools.callNames(); len(got) != 0 {
		t.Fatalf("tool calls=%v", got)
	}
	if got := control.finishCallSnapshot(); len(got) != 0 {
		t.Fatalf("finish calls=%v", got)
	}
	if entries, _ := store.Load(context.Background(), "c1_child"); len(entries) != 1 {
		t.Fatalf("persisted entries=%d, want only user message", len(entries))
	}
}

func TestChildRejectsUnconfiguredToolBeforeDispatch(t *testing.T) {
	store := &memoryStore{entries: map[session.ID][]session.Entry{"c1_child": {}}}
	models := &sequenceModel{responses: []model.Response{{Message: model.Message{
		Role: model.RoleAssistant, ToolCalls: []tool.Call{{ID: "echo-call", Name: "echo", Arguments: json.RawMessage(`{}`)}},
	}}}}
	tools := &subagentRuntimeTools{}
	control := &subagentRuntimeControl{}
	runtime := newSubagentTestRuntime(t, Config{MaxRounds: 2}, models, tools, store, control)

	_, err := runtime.executeFrame(context.Background(), agent.Turn{SessionID: "c1_child", Input: "task"}, nil,
		childTestFrame("c1_child", childSubmitToolName))
	if !errors.Is(err, ErrToolNotAllowed) {
		t.Fatalf("error=%v", err)
	}
	if got := tools.callNames(); len(got) != 0 {
		t.Fatalf("tool calls=%v", got)
	}
	if entries, _ := store.Load(context.Background(), "c1_child"); len(entries) != 1 {
		t.Fatalf("persisted entries=%d, want only user message", len(entries))
	}
}

func TestRootModelViewExcludesChildSubmitTool(t *testing.T) {
	store := &memoryStore{entries: map[session.ID][]session.Entry{"c0_root": {}}}
	models := &sequenceModel{responses: []model.Response{{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("done")}}}}
	tools := &subagentRuntimeTools{}
	control := &subagentRuntimeControl{}
	runtime := newSubagentTestRuntime(t, Config{MaxRounds: 1}, models, tools, store, control)

	if _, err := runtime.Run(context.Background(), agent.Turn{SessionID: "c0_root", Input: "task"}); err != nil {
		t.Fatal(err)
	}
	models.mu.Lock()
	requests := append([]model.Request(nil), models.requests...)
	models.mu.Unlock()
	if len(requests) != 1 || !reflect.DeepEqual(definitionNames(requests[0].Tools), []string{"echo"}) {
		t.Fatalf("root model tools=%v", definitionNames(requests[0].Tools))
	}
}

func TestChildSubmissionConfirmationWaitsForToolResultPersistence(t *testing.T) {
	persistErr := errors.New("tool result persistence failed")
	store := &failingAppendStore{
		memoryStore: &memoryStore{entries: map[session.ID][]session.Entry{"c1_child": {}}},
		failAt:      3,
		err:         persistErr,
	}
	models := &sequenceModel{responses: []model.Response{{Message: model.Message{
		Role: model.RoleAssistant, ToolCalls: []tool.Call{{ID: "submit-call", Name: childSubmitToolName, Arguments: json.RawMessage(`{"result":"done"}`)}},
	}}}}
	tools := &subagentRuntimeTools{}
	control := &subagentRuntimeControl{
		finish:   sessioncontrol.FinishIntent{ToolCallID: "submit-call", Result: "done"},
		finishOK: true,
	}
	runtime := newSubagentTestRuntime(t, Config{MaxRounds: 1}, models, tools, store, control)
	frame := childTestFrame("c1_child", childSubmitToolName)

	_, err := runtime.executeFrame(context.Background(), agent.Turn{SessionID: "c1_child", Input: "task"}, nil, frame)
	if !errors.Is(err, persistErr) {
		t.Fatalf("error=%v", err)
	}
	if frame.confirmed != nil {
		t.Fatalf("submission confirmed before persistence: %#v", frame.confirmed)
	}
	if got := control.finishCallSnapshot(); len(got) != 0 {
		t.Fatalf("finish calls=%v", got)
	}
	if got := tools.callNames(); !reflect.DeepEqual(got, []string{childSubmitToolName}) {
		t.Fatalf("tool calls=%v", got)
	}
}

func newSubagentTestRuntime(
	t *testing.T,
	config Config,
	models model.Runtime,
	tools tool.Runtime,
	store session.Store,
	control sessioncontrol.Control,
) *runtime {
	t.Helper()
	exports, cleanup, err := New(context.Background(), withState(t, config, Dependencies{
		Model: models, Tools: tools, Store: store, Assets: newMemoryAssets(), Prompt: passthroughPrompt{}, Control: control,
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if cleanup != nil {
			if err := cleanup(ctx); err != nil {
				t.Errorf("cleanup runtime: %v", err)
			}
		}
	})
	return exports.Runtime.(*runtime)
}

func childTestFrame(id session.ID, toolNames ...string) *turnFrame {
	return newChildFrame(sessioncontrol.Task{
		Handle:     sessioncontrol.Handle{SessionID: id, Token: 1, Child: true},
		ToolNames:  toolNames,
		SubmitTool: childSubmitToolName,
	})
}

func definitionNames(definitions []tool.Definition) []string {
	result := make([]string, len(definitions))
	for i, definition := range definitions {
		result[i] = definition.Name
	}
	return result
}
