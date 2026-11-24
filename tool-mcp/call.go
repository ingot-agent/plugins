package toolmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ingot-agent/sdk/tool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcpTool is one remote MCP tool exposed as a standard tool. Its Definition is
// a construction-time snapshot: the tool set cannot change without a Runtime
// restart, which is why configuration reports restart_required.
type mcpTool struct {
	platform   *platform
	server     string
	wireName   string
	definition tool.Definition
}

var _ tool.Tool = (*mcpTool)(nil)

func (t *mcpTool) Definition() tool.Definition {
	definition := t.definition
	definition.InputSchema = append(json.RawMessage(nil), t.definition.InputSchema...)
	return definition
}

func (t *mcpTool) Invoke(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
	if ctx == nil {
		return tool.Result{}, fmt.Errorf("tool.mcp: nil context")
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	if invocation.Call.Name != t.definition.Name {
		return tool.Result{}, fmt.Errorf("tool.mcp: tool name %q does not match %q", invocation.Call.Name, t.definition.Name)
	}

	arguments, err := decodeArguments(invocation.Call.Arguments)
	if err != nil {
		return tool.Result{}, err
	}

	config := t.platform.currentConfig()
	server, ok := config.server(t.server)
	if !ok {
		return tool.Result{}, fmt.Errorf("tool.mcp: server %q is no longer configured", t.server)
	}
	release, err := t.platform.runtimeFor(t.server).acquireSlot(ctx, server.MaxConcurrentCalls)
	if err != nil {
		return tool.Result{}, fmt.Errorf("tool.mcp: waiting for a slot on server %q: %w", t.server, err)
	}
	defer release()

	session, err := t.platform.acquire(ctx, t.server)
	if err != nil {
		return tool.Result{}, err
	}
	timeout := time.Duration(server.CallTimeoutSeconds) * time.Second
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	params := &mcp.CallToolParams{
		Name:      t.wireName,
		Arguments: arguments,
	}
	if invocation.Call.ID != "" {
		params.SetProgressToken(invocation.Call.ID)
	}

	result, err := session.session.CallTool(callCtx, params)
	if err != nil {
		if callCtx.Err() != nil {
			return tool.Result{}, fmt.Errorf("tool.mcp: call %q on server %q interrupted: %w", t.wireName, t.server, callCtx.Err())
		}
		return tool.Result{}, fmt.Errorf("tool.mcp: call %q on server %q failed: %w", t.wireName, t.server, err)
	}
	return mapCallResult(t.server, result, config.MaxResultBytes), nil
}

// decodeArguments converts the validated tool arguments into the value handed
// to the MCP server. json.Number preserves numeric precision that a float64
// round trip would corrupt.
func decodeArguments(raw json.RawMessage) (any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return map[string]any{}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("tool.mcp: decode arguments: %w: %w", ErrInvalidArguments, err)
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, fmt.Errorf("tool.mcp: arguments must be a JSON object: %w", ErrInvalidArguments)
	}
	return value, nil
}
