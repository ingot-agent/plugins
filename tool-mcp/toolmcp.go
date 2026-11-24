// Package toolmcp exposes tools of external Model Context Protocol servers as
// ordinary Ingot tools. It is an MCP client gateway: it never implements an
// MCP server and never lets a remote server define Ingot plugins.
//
// Tool definitions are a construction-time snapshot. Discovery is performed by
// the config and refresh Operations and cached in the Plugin's state scope, so
// a changed tool set requires a Runtime restart. This keeps the standard tool
// Runtime's argument validation, per-tool approval, and observability intact.
package toolmcp

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"

	ingotabi "github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/ingot-abi/state"
	"github.com/ingot-agent/sdk/httpx"
	"github.com/ingot-agent/sdk/logging"
	"github.com/ingot-agent/sdk/observation"
	"github.com/ingot-agent/sdk/operation"
	"github.com/ingot-agent/sdk/tool"
)

// Dependencies supplies this Plugin's state scope and optional capabilities.
type Dependencies struct {
	State state.Scope
	// HTTP is used only by servers with the http transport. A composition that
	// configures no such server does not need to provide it.
	HTTP        ingotabi.Optional[httpx.Client]
	Logger      ingotabi.Optional[logging.Logger]
	Observation ingotabi.Optional[observation.Consumer]
}

// Exports contains the snapshotted remote tools, the diagnostic status tool,
// and this Plugin's configuration Operations.
type Exports struct {
	Tools      []tool.Tool
	Operations []operation.Operation
}

// New constructs the gateway. Construction reads persisted configuration and
// the cached tool snapshot only: it starts no process, opens no connection,
// and starts no background goroutine. Connections are established lazily by
// the first tool call.
func New(ctx context.Context, deps Dependencies) (Exports, ingotabi.Cleanup, error) {
	if ctx == nil {
		return Exports{}, nil, ErrInvalidDependencies
	}
	if err := ctx.Err(); err != nil {
		return Exports{}, nil, err
	}
	if isNil(deps.State) {
		return Exports{}, nil, fmt.Errorf("state scope is required: %w", ErrInvalidDependencies)
	}
	stateDir := deps.State.Dir()
	if strings.TrimSpace(stateDir) == "" {
		return Exports{}, nil, fmt.Errorf("state scope is empty: %w", ErrInvalidDependencies)
	}

	logger := logging.Discard()
	if deps.Logger.Valid {
		if isNil(deps.Logger.Value) {
			return Exports{}, nil, fmt.Errorf("logger is typed nil: %w", ErrInvalidDependencies)
		}
		logger = deps.Logger.Value
	}
	var observer observation.Consumer
	if deps.Observation.Valid {
		if isNil(deps.Observation.Value) {
			return Exports{}, nil, fmt.Errorf("observation consumer is typed nil: %w", ErrInvalidDependencies)
		}
		observer = deps.Observation.Value
	}
	if deps.HTTP.Valid && isNil(deps.HTTP.Value) {
		return Exports{}, nil, fmt.Errorf("http client is typed nil: %w", ErrInvalidDependencies)
	}

	rawConfig, err := loadConfig(stateDir)
	if err != nil {
		return Exports{}, nil, err
	}
	config, err := normalizeConfig(rawConfig)
	if err != nil {
		return Exports{}, nil, err
	}

	instance := newPlatform(stateDir, deps.HTTP, logger, observer, config)

	snapshot, snapshotErr := loadSnapshot(stateDir)
	switch {
	case snapshotErr != nil:
		logger.Warn(ctx, "MCP tool snapshot is unreadable; no MCP tools are exported",
			logging.String("error", sanitizeMessage(snapshotErr.Error())))
		snapshot = nil
	case snapshot != nil:
		if hash, hashErr := config.configHash(); hashErr == nil && snapshot.ConfigHash != hash {
			snapshot.Stale = true
			logger.Warn(ctx, "MCP tool snapshot does not match the current configuration; run the tool-mcp refresh operation")
		}
	case len(config.enabledServers()) > 0:
		logger.Info(ctx, "no MCP tool snapshot exists yet; run the tool-mcp refresh operation to discover tools")
	}

	tools := buildTools(ctx, instance, config, snapshot, logger)
	// exportedNames is the tool set this image actually exposes. Operations
	// compare discovery against it, not against the file they read at
	// invocation start, so restart_required describes the running image.
	exportedNames := make([]string, 0, len(tools))
	for _, entry := range tools {
		exportedNames = append(exportedNames, entry.Definition().Name)
	}
	if config.exposeStatusTool() {
		if hasToolName(tools, statusToolName) {
			logger.Warn(ctx, "an MCP tool already uses the status tool name; mcp_status is not exported")
		} else {
			tools = append(tools, &statusTool{platform: instance, snapshot: snapshot, exported: exportedNames})
		}
	}
	sort.Slice(tools, func(i, j int) bool {
		return tools[i].Definition().Name < tools[j].Definition().Name
	})

	operations := []operation.Operation{
		&setupOperation{platform: instance, exported: exportedNames},
		&refreshOperation{platform: instance, exported: exportedNames},
	}

	cleanup := func(context.Context) error {
		instance.closeAll()
		return nil
	}
	return Exports{Tools: tools, Operations: operations}, cleanup, nil
}

// buildTools re-validates every snapshotted tool before exporting it, so a
// hand-edited or truncated snapshot can never make the Runtime Image fail to
// start.
func buildTools(ctx context.Context, instance *platform, config Config, snapshot *Snapshot, logger logging.Logger) []tool.Tool {
	if snapshot == nil {
		return nil
	}
	taken := make(map[string]bool)
	var result []tool.Tool
	for _, server := range snapshot.Servers {
		serverConfig, ok := config.server(server.Name)
		if !ok || !serverConfig.enabled() {
			continue
		}
		if server.Transport != serverConfig.Transport {
			continue
		}
		for _, discovered := range server.Tools {
			if taken[discovered.ModelName] {
				continue
			}
			if !modelToolNamePattern.MatchString(discovered.ModelName) {
				logger.Warn(ctx, "skipping snapshot tool with an invalid name",
					logging.String("tool", discovered.ModelName))
				continue
			}
			if strings.TrimSpace(discovered.WireName) == "" {
				continue
			}
			schema, err := normalizeInputSchema(discovered.Schema)
			if err != nil {
				logger.Warn(ctx, "skipping snapshot tool with an invalid schema",
					logging.String("tool", discovered.ModelName),
					logging.String("error", sanitizeMessage(err.Error())))
				continue
			}
			if err := compileSchema(discovered.ModelName, schema); err != nil {
				logger.Warn(ctx, "skipping snapshot tool with an uncompilable schema",
					logging.String("tool", discovered.ModelName),
					logging.String("error", sanitizeMessage(err.Error())))
				continue
			}
			description := strings.TrimSpace(discovered.Description)
			if description == "" {
				description = describeTool(server.Name, discovered.WireName, "")
			}
			taken[discovered.ModelName] = true
			result = append(result, &mcpTool{
				platform: instance,
				server:   server.Name,
				wireName: discovered.WireName,
				definition: tool.Definition{
					Name:        discovered.ModelName,
					Description: description,
					InputSchema: schema,
				},
			})
		}
	}
	return result
}

// hasToolName reports whether an exported tool already uses name.
func hasToolName(tools []tool.Tool, name string) bool {
	for _, entry := range tools {
		if entry.Definition().Name == name {
			return true
		}
	}
	return false
}

// isNil reports whether value is nil or a typed nil pointer/interface.
func isNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
