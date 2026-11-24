package toolmcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/tool"
)

const statusToolName = "mcp_status"

// statusTool reports MCP connectivity without touching the network. It exists
// because a snapshot that produced no tools must still be diagnosable from
// inside the agent.
type statusTool struct {
	platform *platform
	// snapshot is the construction-time view, used when the file cannot be
	// read anymore.
	snapshot *Snapshot
	// exported is the tool set this Runtime Image actually exposes, excluding
	// the status tool itself.
	exported []string
}

var _ tool.Tool = (*statusTool)(nil)

func (t *statusTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        statusToolName,
		Description: "Report configured MCP servers: connection state, exported tools, and discovery problems. Use when an MCP tool is missing or failing.",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`),
	}
}

func (t *statusTool) Invoke(ctx context.Context, _ tool.Invocation) (tool.Result, error) {
	if ctx == nil {
		return tool.Result{}, fmt.Errorf("tool.mcp: nil context")
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	encoded, err := json.MarshalIndent(t.report(), "", "  ")
	if err != nil {
		return tool.Result{}, fmt.Errorf("tool.mcp: encode status: %w", err)
	}
	return tool.Result{Content: content.FromText(string(encoded))}, nil
}

type statusReport struct {
	SnapshotGeneratedAt string                 `json:"snapshot_generated_at,omitempty"`
	SnapshotStale       bool                   `json:"snapshot_stale"`
	RestartRequired     bool                   `json:"restart_required"`
	ExportedToolCount   int                    `json:"exported_tool_count"`
	Servers             []serverStatusSnapshot `json:"servers"`
}

type serverStatusSnapshot struct {
	serverStatus
	ExportedTools []string       `json:"exported_tools,omitempty"`
	RejectedTools []RejectedTool `json:"rejected_tools,omitempty"`
	DiscoveryErr  string         `json:"discovery_error,omitempty"`
}

// report reads the current snapshot from disk so that a completed refresh is
// visible without a restart. restart_required compares the tools this image
// actually exports with the tools the snapshot now describes.
func (t *statusTool) report() statusReport {
	config := t.platform.currentConfig()
	snapshot := t.snapshot
	if loaded, err := loadSnapshot(t.platform.stateDir); err == nil && loaded != nil {
		snapshot = loaded
	}

	discovered := []string{}
	if snapshot != nil {
		discovered = snapshot.toolNames()
	}
	stale := false
	if snapshot != nil {
		if hash, err := config.configHash(); err == nil && snapshot.ConfigHash != hash {
			stale = true
		}
	}

	report := statusReport{
		SnapshotStale:     stale,
		RestartRequired:   !equalStringSets(t.exported, discovered),
		ExportedToolCount: len(t.exported),
	}
	if snapshot != nil {
		report.SnapshotGeneratedAt = snapshot.GeneratedAt
	}
	for _, runtime := range t.platform.statuses() {
		entry := serverStatusSnapshot{serverStatus: runtime}
		if snapshot != nil {
			for _, server := range snapshot.Servers {
				if server.Name != runtime.Name {
					continue
				}
				entry.DiscoveryErr = server.Error
				entry.RejectedTools = server.Rejected
				for _, discovered := range server.Tools {
					entry.ExportedTools = append(entry.ExportedTools, discovered.ModelName)
				}
			}
		}
		report.Servers = append(report.Servers, entry)
	}
	if report.Servers == nil {
		report.Servers = []serverStatusSnapshot{}
	}
	return report
}

// equalStringSets compares two string collections as sets.
func equalStringSets(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	seen := make(map[string]int, len(left))
	for _, value := range left {
		seen[value]++
	}
	for _, value := range right {
		seen[value]--
		if seen[value] < 0 {
			return false
		}
	}
	return true
}
