package toolsubagent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/execution"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/tool"
)

type fakeChildren struct {
	createScope   execution.Scope
	createRequest agent.ChildRequest
	submitScope   execution.Scope
	submitCallID  string
	submitResult  string
	typesErr      error
	createErr     error
}

func (f *fakeChildren) Types(ctx context.Context, _ execution.Scope) ([]agent.AgentTypeInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []agent.AgentTypeInfo{{Name: "coder", Description: "code"}}, f.typesErr
}

func (f *fakeChildren) CreateChild(_ context.Context, scope execution.Scope, request agent.ChildRequest) (agent.ChildSnapshot, error) {
	f.createScope = scope
	f.createRequest = request
	return agent.ChildSnapshot{SessionID: "c1_child", StateConfirmed: true}, f.createErr
}

func (*fakeChildren) Check(context.Context, execution.Scope, session.ID, bool) (agent.ChildSnapshot, error) {
	return agent.ChildSnapshot{SessionID: "c1_child", StateConfirmed: true}, nil
}

func (*fakeChildren) Wait(context.Context, execution.Scope, session.ID) (agent.ChildSnapshot, error) {
	return agent.ChildSnapshot{SessionID: "c1_child", StateConfirmed: true}, nil
}

func (*fakeChildren) List(context.Context, execution.Scope, agent.ChildrenPageRequest) (agent.ChildrenPage, error) {
	return agent.ChildrenPage{}, nil
}

func (*fakeChildren) Cancel(context.Context, execution.Scope, session.ID) (agent.CancelResult, error) {
	return agent.CancelResult{}, nil
}

func (f *fakeChildren) SubmitResult(_ context.Context, scope execution.Scope, callID, result string) error {
	f.submitScope = scope
	f.submitCallID = callID
	f.submitResult = result
	return nil
}

func TestToolSetAndInvocationIdentity(t *testing.T) {
	children := &fakeChildren{}
	exports, cleanup, err := New(context.Background(), Dependencies{Children: children})
	if err != nil {
		t.Fatal(err)
	}
	if cleanup != nil {
		t.Fatal("cleanup must be nil")
	}
	names := make([]string, 0, len(exports.Tools))
	byName := map[string]tool.Tool{}
	for _, candidate := range exports.Tools {
		definition := candidate.Definition()
		names = append(names, definition.Name)
		byName[definition.Name] = candidate
	}
	want := []string{"list_agent_types", "spawn_agent", "check_agent", "wait_agent", "list_agents", "cancel_agent", "submit_agent_result"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("tool names=%v want=%v", names, want)
	}
	scope := execution.Scope{SessionID: "c0_root"}
	_, err = byName["spawn_agent"].Invoke(context.Background(), tool.Invocation{Scope: scope, Call: tool.Call{
		ID: "spawn-call", Name: "spawn_agent",
		Arguments: json.RawMessage(`{"agent_type":"coder","task":"implement","workspace_root":"C:\\work"}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if children.createScope != scope || !children.createRequest.Wait || children.createRequest.Workspace == nil || children.createRequest.Workspace.Root != `C:\work` {
		t.Fatalf("create scope=%#v request=%#v", children.createScope, children.createRequest)
	}
	childScope := execution.Scope{SessionID: "c1_child"}
	_, err = byName["submit_agent_result"].Invoke(context.Background(), tool.Invocation{Scope: childScope, Call: tool.Call{
		ID: "submit-call", Name: "submit_agent_result", Arguments: json.RawMessage(`{"result":"done"}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if children.submitScope != childScope || children.submitCallID != "submit-call" || children.submitResult != "done" {
		t.Fatalf("submit scope=%#v call=%q result=%q", children.submitScope, children.submitCallID, children.submitResult)
	}
}

func TestParentOperationErrorsBecomeBusinessResults(t *testing.T) {
	children := &fakeChildren{createErr: agent.ErrChildCapacity}
	exports, _, err := New(context.Background(), Dependencies{Children: children})
	if err != nil {
		t.Fatal(err)
	}
	var spawn tool.Tool
	for _, candidate := range exports.Tools {
		if candidate.Definition().Name == "spawn_agent" {
			spawn = candidate
		}
	}
	result, err := spawn.Invoke(context.Background(), tool.Invocation{Scope: execution.Scope{SessionID: "c0_root"}, Call: tool.Call{
		ID: "call", Name: "spawn_agent", Arguments: json.RawMessage(`{"agent_type":"coder","task":"implement","workspace_root":"C:\\work"}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		OK    bool                  `json:"ok"`
		Error agent.ChildDiagnostic `json:"error"`
	}
	if err := json.Unmarshal([]byte(result.Content[0].Text), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.OK || payload.Error.Code != "capacity" {
		t.Fatalf("payload=%#v", payload)
	}
}

func TestListAgentTypesUsesStructuredResults(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		exports, _, err := New(context.Background(), Dependencies{Children: &fakeChildren{}})
		if err != nil {
			t.Fatal(err)
		}
		result, err := toolByName(t, exports.Tools, "list_agent_types").Invoke(context.Background(), tool.Invocation{
			Scope: execution.Scope{SessionID: "c0_root"},
			Call:  tool.Call{ID: "call", Name: "list_agent_types", Arguments: json.RawMessage(`{}`)},
		})
		if err != nil {
			t.Fatal(err)
		}
		var payload struct {
			OK    bool                  `json:"ok"`
			Types []agent.AgentTypeInfo `json:"types"`
		}
		if err := json.Unmarshal([]byte(result.Content[0].Text), &payload); err != nil {
			t.Fatal(err)
		}
		if !payload.OK || len(payload.Types) != 1 || payload.Types[0].Name != "coder" {
			t.Fatalf("payload=%#v", payload)
		}
	})

	t.Run("business failure", func(t *testing.T) {
		exports, _, err := New(context.Background(), Dependencies{Children: &fakeChildren{typesErr: agent.ErrChildUnauthorized}})
		if err != nil {
			t.Fatal(err)
		}
		result, err := toolByName(t, exports.Tools, "list_agent_types").Invoke(context.Background(), tool.Invocation{
			Scope: execution.Scope{SessionID: "c0_root"},
			Call:  tool.Call{ID: "call", Name: "list_agent_types", Arguments: json.RawMessage(`{}`)},
		})
		if err != nil {
			t.Fatal(err)
		}
		var payload struct {
			OK    bool                  `json:"ok"`
			Error agent.ChildDiagnostic `json:"error"`
		}
		if err := json.Unmarshal([]byte(result.Content[0].Text), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.OK || payload.Error.Code != "unauthorized" {
			t.Fatalf("payload=%#v", payload)
		}
	})
}

func TestListAgentTypesPropagatesCallerCancellation(t *testing.T) {
	exports, _, err := New(context.Background(), Dependencies{Children: &fakeChildren{}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = toolByName(t, exports.Tools, "list_agent_types").Invoke(ctx, tool.Invocation{
		Scope: execution.Scope{SessionID: "c0_root"},
		Call:  tool.Call{ID: "call", Name: "list_agent_types", Arguments: json.RawMessage(`{}`)},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}

func TestCanceledConstructionAndTypedNil(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := New(ctx, Dependencies{Children: &fakeChildren{}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled New error=%v", err)
	}
	var typedNil *fakeChildren
	if _, _, err := New(context.Background(), Dependencies{Children: typedNil}); !errors.Is(err, ErrInvalidDependencies) {
		t.Fatalf("typed nil error=%v", err)
	}
}

func toolByName(t *testing.T, tools []tool.Tool, name string) tool.Tool {
	t.Helper()
	for _, candidate := range tools {
		if candidate.Definition().Name == name {
			return candidate
		}
	}
	t.Fatalf("tool %q not found", name)
	return nil
}

var _ agent.Children = (*fakeChildren)(nil)
