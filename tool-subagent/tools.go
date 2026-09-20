// Package toolsubagent exposes child-agent management through ordinary tools.
package toolsubagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	ingotabi "github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/tool"
	"github.com/ingot-agent/sdk/workspace"
)

var ErrInvalidDependencies = errors.New("invalid tool.subagent dependencies")

// Dependencies contains the child management capability supplied by the
// selected Agent implementation.
type Dependencies struct {
	Children agent.Children
}

// Exports contributes all child management tools to tool.runtime.
type Exports struct {
	Tools []tool.Tool
}

type childTool struct {
	definition tool.Definition
	invoke     func(context.Context, tool.Invocation) (tool.Result, error)
}

func (t *childTool) Definition() tool.Definition {
	return tool.Definition{
		Name: t.definition.Name, Description: t.definition.Description,
		InputSchema: append(json.RawMessage(nil), t.definition.InputSchema...),
	}
}

func (t *childTool) Invoke(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
	return t.invoke(ctx, invocation)
}

// New creates the fixed child management tool set.
func New(ctx context.Context, deps Dependencies) (Exports, ingotabi.Cleanup, error) {
	if ctx == nil || isNil(deps.Children) {
		return Exports{}, nil, fmt.Errorf("construct tool.subagent: %w", ErrInvalidDependencies)
	}
	if err := ctx.Err(); err != nil {
		return Exports{}, nil, err
	}
	tools := []tool.Tool{
		newTool("list_agent_types", "List child-agent types allowed for this execution.", `{
  "type":"object","additionalProperties":false,
  "required":[],
  "properties":{}
}`, func(ctx context.Context, invocation tool.Invocation) (any, error) {
			types, err := deps.Children.Types(ctx, invocation.Scope)
			if err != nil {
				if contextErr := propagateContext(ctx, err); contextErr != nil {
					return nil, contextErr
				}
				return businessFailure(errorCode(err), err.Error(), nil), nil
			}
			return map[string]any{"ok": true, "types": types}, nil
		}),
		newTool("spawn_agent", "Create and run one single-Turn child-agent Session.", `{
  "type":"object","additionalProperties":false,
  "required":["agent_type","task","workspace_root"],
  "properties":{
    "agent_type":{"type":"string","minLength":1},
    "task":{"type":"string","minLength":1},
    "context":{"type":"string"},
    "workspace_root":{"type":"string","minLength":1},
    "wait":{"type":"boolean"}
  }
}`, func(ctx context.Context, invocation tool.Invocation) (any, error) {
			var args struct {
				AgentType     string `json:"agent_type"`
				Task          string `json:"task"`
				Context       string `json:"context"`
				WorkspaceRoot string `json:"workspace_root"`
				Wait          *bool  `json:"wait"`
			}
			if err := decodeArguments(invocation, &args); err != nil {
				return nil, err
			}
			wait := true
			if args.Wait != nil {
				wait = *args.Wait
			}
			snapshot, err := deps.Children.CreateChild(ctx, invocation.Scope, agent.ChildRequest{
				AgentType: args.AgentType, Task: args.Task, Context: args.Context,
				Workspace: &workspace.Binding{Root: args.WorkspaceRoot}, Wait: wait,
			})
			return snapshotEnvelope(snapshot, err), propagateContext(ctx, err)
		}),
		newTool("check_agent", "Read a child-agent state without waiting for completion.", `{
  "type":"object","additionalProperties":false,"required":["session_id"],
  "properties":{"session_id":{"type":"string","minLength":1},"include_result":{"type":"boolean"}}
}`, func(ctx context.Context, invocation tool.Invocation) (any, error) {
			var args struct {
				SessionID     session.ID `json:"session_id"`
				IncludeResult bool       `json:"include_result"`
			}
			if err := decodeArguments(invocation, &args); err != nil {
				return nil, err
			}
			snapshot, err := deps.Children.Check(ctx, invocation.Scope, args.SessionID, args.IncludeResult)
			return snapshotEnvelope(snapshot, err), propagateContext(ctx, err)
		}),
		newTool("wait_agent", "Wait for a current child execution, or read persisted state once when it is not running in this process.", `{
  "type":"object","additionalProperties":false,"required":["session_id"],
  "properties":{"session_id":{"type":"string","minLength":1},"timeout_ms":{"type":"integer","minimum":1,"maximum":3600000}}
}`, func(ctx context.Context, invocation tool.Invocation) (any, error) {
			var args struct {
				SessionID session.ID `json:"session_id"`
				TimeoutMS int64      `json:"timeout_ms"`
			}
			if err := decodeArguments(invocation, &args); err != nil {
				return nil, err
			}
			waitCtx := ctx
			var cancel context.CancelFunc
			if args.TimeoutMS > 0 {
				waitCtx, cancel = context.WithTimeout(ctx, time.Duration(args.TimeoutMS)*time.Millisecond)
				defer cancel()
			}
			snapshot, err := deps.Children.Wait(waitCtx, invocation.Scope, args.SessionID)
			if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
				return businessFailure("wait_timeout", "wait timeout elapsed; the child was not canceled", &snapshot), nil
			}
			return snapshotEnvelope(snapshot, err), propagateContext(ctx, err)
		}),
		newTool("list_agents", "List one page of direct child-agent Sessions.", `{
  "type":"object","additionalProperties":false,
  "properties":{
    "parent_session_id":{"type":"string"},
    "cursor":{"type":"string"},
    "page_size":{"type":"integer","minimum":1,"maximum":100}
  }
}`, func(ctx context.Context, invocation tool.Invocation) (any, error) {
			var args struct {
				ParentSessionID session.ID `json:"parent_session_id"`
				Cursor          string     `json:"cursor"`
				PageSize        int        `json:"page_size"`
			}
			if err := decodeArguments(invocation, &args); err != nil {
				return nil, err
			}
			page, err := deps.Children.List(ctx, invocation.Scope, agent.ChildrenPageRequest{ParentSessionID: args.ParentSessionID, Cursor: args.Cursor, PageSize: args.PageSize})
			if err != nil {
				if contextErr := propagateContext(ctx, err); contextErr != nil {
					return nil, contextErr
				}
				return businessFailure(errorCode(err), err.Error(), nil), nil
			}
			return map[string]any{"ok": true, "page": page}, nil
		}),
		newTool("cancel_agent", "Cancel queued or working tasks in a child-agent branch.", `{
  "type":"object","additionalProperties":false,"required":["session_id"],
  "properties":{"session_id":{"type":"string","minLength":1}}
}`, func(ctx context.Context, invocation tool.Invocation) (any, error) {
			var args struct {
				SessionID session.ID `json:"session_id"`
			}
			if err := decodeArguments(invocation, &args); err != nil {
				return nil, err
			}
			result, err := deps.Children.Cancel(ctx, invocation.Scope, args.SessionID)
			if err != nil {
				if contextErr := propagateContext(ctx, err); contextErr != nil {
					return nil, contextErr
				}
				return businessFailure(errorCode(err), err.Error(), nil), nil
			}
			return map[string]any{"ok": true, "cancel": result}, nil
		}),
		&childTool{definition: definition("submit_agent_result", "Submit the final report and end this child Turn.", `{
  "type":"object","additionalProperties":false,"required":["result"],
  "properties":{"result":{"type":"string","minLength":1}}
}`), invoke: func(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
			var args struct {
				Result string `json:"result"`
			}
			if err := decodeArguments(invocation, &args); err != nil {
				return tool.Result{}, err
			}
			if err := deps.Children.SubmitResult(ctx, invocation.Scope, invocation.Call.ID, args.Result); err != nil {
				return tool.Result{}, err
			}
			return jsonResult(map[string]any{"accepted": true})
		}},
	}
	return Exports{Tools: tools}, nil, nil
}

func newTool(name, description, schema string, invoke func(context.Context, tool.Invocation) (any, error)) tool.Tool {
	return &childTool{definition: definition(name, description, schema), invoke: func(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
		value, err := invoke(ctx, invocation)
		if err != nil {
			return tool.Result{}, err
		}
		return jsonResult(value)
	}}
}

func definition(name, description, schema string) tool.Definition {
	return tool.Definition{Name: name, Description: description, InputSchema: json.RawMessage(schema)}
}

func decodeArguments(invocation tool.Invocation, target any) error {
	if err := json.Unmarshal(invocation.Call.Arguments, target); err != nil {
		return fmt.Errorf("decode %s arguments: %w", invocation.Call.Name, err)
	}
	return nil
}

func jsonResult(value any) (tool.Result, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Content: content.FromText(string(raw))}, nil
}

func snapshotEnvelope(snapshot agent.ChildSnapshot, err error) map[string]any {
	if err == nil {
		return map[string]any{"ok": true, "child": snapshot}
	}
	return businessFailure(errorCode(err), err.Error(), &snapshot)
}

func businessFailure(code, message string, snapshot *agent.ChildSnapshot) map[string]any {
	result := map[string]any{"ok": false, "error": agent.ChildDiagnostic{Code: code, Message: message}}
	if snapshot != nil && snapshot.SessionID != "" {
		result["child"] = *snapshot
	}
	return result
}

func errorCode(err error) string {
	switch {
	case errors.Is(err, agent.ErrChildUnsupported):
		return "unsupported"
	case errors.Is(err, agent.ErrChildUnauthorized):
		return "unauthorized"
	case errors.Is(err, agent.ErrChildCapacity):
		return "capacity"
	case errors.Is(err, agent.ErrChildInvalidState):
		return "invalid_state"
	case errors.Is(err, agent.ErrChildSubmission):
		return "invalid_submission"
	case errors.Is(err, session.ErrNotFound):
		return "not_found"
	default:
		return "operation_failed"
	}
}

func propagateContext(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

var _ tool.Tool = (*childTool)(nil)
