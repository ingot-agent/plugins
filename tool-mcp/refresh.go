package toolmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ingot-agent/sdk/operation"
)

const (
	setupOperationName   = "config"
	setupOperationGroup  = "tool-mcp"
	refreshOperationName = "refresh"
)

// serverReport is the secret-free per-server summary returned by both
// configuration Operations.
type serverReport struct {
	Name        string `json:"name"`
	Transport   string `json:"transport"`
	Status      string `json:"status"`
	Tools       int    `json:"tools"`
	Rejected    int    `json:"rejected"`
	SchemaBytes int    `json:"schema_bytes"`
	Error       string `json:"error,omitempty"`
}

// refreshOutput is the machine-readable outcome of a discovery pass.
type refreshOutput struct {
	RestartRequired bool           `json:"restart_required"`
	Added           []string       `json:"added"`
	Removed         []string       `json:"removed"`
	Servers         []serverReport `json:"servers"`
}

func buildServerReports(snapshot *Snapshot) []serverReport {
	reports := make([]serverReport, 0, len(snapshot.Servers))
	for _, server := range snapshot.Servers {
		reports = append(reports, serverReport{
			Name:        server.Name,
			Transport:   server.Transport,
			Status:      server.Status,
			Tools:       server.ToolCount,
			Rejected:    len(server.Rejected),
			SchemaBytes: server.SchemaBytes,
			Error:       server.Error,
		})
	}
	return reports
}

// approvalHints returns the tool-name prefixes a user can paste into the
// approval interceptor policy to gate every tool of a server at once.
func approvalHints(snapshot *Snapshot) []string {
	hints := []string{}
	for _, server := range snapshot.Servers {
		if len(server.Tools) == 0 {
			continue
		}
		hints = append(hints, toolPrefixHint(server.Tools[0]))
	}
	return hints
}

// toolPrefixHint derives the server-scoped approval prefix from an exported
// tool name. The trailing wire-name segment is removed when it is still
// recognizable, which yields "mcp_github_*" for "mcp_github_create_issue".
func toolPrefixHint(tool ToolSnapshot) string {
	if wire := sanitizeSegment(tool.WireName); wire != "" {
		if trimmed := strings.TrimSuffix(tool.ModelName, wire); trimmed != tool.ModelName && strings.HasSuffix(trimmed, "_") {
			return trimmed + "*"
		}
	}
	if index := strings.LastIndex(tool.ModelName, "_"); index > 0 {
		return tool.ModelName[:index+1] + "*"
	}
	return tool.ModelName + "_*"
}

func diffOrEmpty(previous, next *Snapshot) (added, removed []string) {
	added, removed = diffToolNames(previous, next)
	if added == nil {
		added = []string{}
	}
	if removed == nil {
		removed = []string{}
	}
	return added, removed
}

// discoverAndSave runs discovery and persists the result. A failed snapshot
// write is reported, but the in-memory report is still returned so the caller
// can describe what was discovered.
func discoverAndSave(ctx context.Context, p *platform, config Config, previous *Snapshot) (*Snapshot, error) {
	// Only one discovery pass may own the snapshot file at a time; concurrent
	// passes would otherwise interleave and let an older result win.
	p.discoverMu.Lock()
	defer p.discoverMu.Unlock()

	snapshot := p.discover(ctx, config, previous)
	if err := saveSnapshot(p.stateDir, snapshot); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

// refreshOperation re-runs discovery with the current configuration and
// reports whether the exported tool set changed.
type refreshOperation struct {
	platform *platform
}

var _ operation.Operation = (*refreshOperation)(nil)

func (*refreshOperation) Definition() operation.Definition {
	return operation.Definition{
		Name:         refreshOperationName,
		Description:  "Re-discover tools from the configured MCP servers and report whether a restart is required.",
		Group:        setupOperationGroup,
		InputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`),
		OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["restart_required","added","removed","servers"],"properties":{"restart_required":{"type":"boolean"},"added":{"type":"array","items":{"type":"string"}},"removed":{"type":"array","items":{"type":"string"}},"servers":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["name","transport","status","tools","rejected","schema_bytes"],"properties":{"name":{"type":"string"},"transport":{"type":"string"},"status":{"type":"string"},"tools":{"type":"integer"},"rejected":{"type":"integer"},"schema_bytes":{"type":"integer"},"error":{"type":"string"}}}}}}`),
	}
}

func (o *refreshOperation) Invoke(ctx context.Context, request operation.Request) (operation.Result, error) {
	if ctx == nil {
		return operation.Result{}, fmt.Errorf("tool.mcp.refresh: nil context")
	}
	if err := ctx.Err(); err != nil {
		return operation.Result{}, err
	}
	if o == nil || o.platform == nil {
		return operation.Result{}, fmt.Errorf("tool.mcp.refresh: platform is required: %w", ErrInvalidDependencies)
	}

	previous, err := loadSnapshot(o.platform.stateDir)
	if err != nil {
		previous = nil
	}
	config := o.platform.currentConfig()
	snapshot, err := discoverAndSave(ctx, o.platform, config, previous)
	if err != nil {
		return operation.Result{}, fmt.Errorf("tool.mcp.refresh: persist snapshot: %w", err)
	}
	added, removed := diffOrEmpty(previous, snapshot)

	output, err := json.Marshal(refreshOutput{
		RestartRequired: len(added) > 0 || len(removed) > 0,
		Added:           added,
		Removed:         removed,
		Servers:         buildServerReports(snapshot),
	})
	if err != nil {
		return operation.Result{}, fmt.Errorf("tool.mcp.refresh: encode output: %w", err)
	}
	return operation.Result{Output: output}, nil
}
