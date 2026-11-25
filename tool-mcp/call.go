package toolmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ingot-agent/sdk/logging"
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
	release, err := t.platform.slotsFor(t.server).acquire(ctx, server.MaxConcurrentCalls)
	if err != nil {
		return tool.Result{}, fmt.Errorf("tool.mcp: waiting for a slot on server %q: %w", t.server, err)
	}
	defer release()

	// The connection is scoped to this call's Session: a shared MCP session
	// would expose one Session's server-side state to another.
	held, err := t.platform.acquire(ctx, t.server, invocation.Scope)
	if err != nil {
		return tool.Result{}, err
	}
	// The lease keeps the connection alive even if this call outlives a
	// reconfiguration that retires the session.
	defer held.release()

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

	result, err := held.conn.session.CallTool(callCtx, params)
	if err != nil {
		if callCtx.Err() != nil {
			return tool.Result{}, fmt.Errorf("tool.mcp: call %q on server %q interrupted: %w", t.wireName, t.server, callCtx.Err())
		}
		// A transport-level failure means the session may be dead. Retire it so
		// the next call reconnects instead of failing forever, and record the
		// outage for backoff and mcp_status.
		key := connectionKey{server: t.server, session: string(invocation.Scope.SessionID)}
		runtime, lookupErr := t.platform.runtimeFor(key, server, "", "")
		if lookupErr == nil && runtime.invalidate(held, err) {
			stderr := held.conn.stderr.String()
			if stderr != "" {
				t.platform.logger.Warn(ctx, "MCP server closed its transport",
					logging.String("server", t.server),
					logging.String("stderr", sanitizeMessage(redactSecrets(server, stderr))))
			}
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
