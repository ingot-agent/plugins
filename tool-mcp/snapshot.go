package toolmcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// snapshotFileName is the Plugin-owned cached tool view. It is produced by
	// discovery and consumed by construction, which is why a changed tool set
	// requires a restart of the Runtime Image.
	snapshotFileName  = "snapshot.json"
	snapshotByteLimit = 8 << 20
	snapshotVersion   = 1
)

const (
	serverStatusOK      = "ok"
	serverStatusError   = "error"
	serverStatusSkipped = "skipped"
)

// Snapshot is the persisted result of one discovery pass over all enabled
// servers. It carries no secrets: environment values and header values are
// never written here.
type Snapshot struct {
	Version     int              `json:"snapshot_version"`
	GeneratedAt string           `json:"generated_at"`
	ConfigHash  string           `json:"config_hash"`
	Stale       bool             `json:"-"`
	Servers     []ServerSnapshot `json:"servers"`
}

// ServerSnapshot is the discovered view of one MCP server.
type ServerSnapshot struct {
	Name        string         `json:"name"`
	Transport   string         `json:"transport"`
	Status      string         `json:"status"`
	Error       string         `json:"error,omitempty"`
	ToolCount   int            `json:"tool_count"`
	SchemaBytes int            `json:"schema_bytes"`
	Tools       []ToolSnapshot `json:"tools"`
	Rejected    []RejectedTool `json:"rejected,omitempty"`
}

// ToolSnapshot is one model-visible tool with its normalized input schema.
type ToolSnapshot struct {
	ModelName   string          `json:"model_name"`
	WireName    string          `json:"wire_name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"`
}

// RejectedTool records a tool that discovery refused to export.
type RejectedTool struct {
	WireName string `json:"wire_name"`
	Reason   string `json:"reason"`
}

// loadSnapshot reads the cached discovery result. A missing file is the normal
// unconfigured state; a corrupt file is returned as an error so that callers
// can degrade explicitly instead of silently exporting nothing.
func loadSnapshot(scope string) (*Snapshot, error) {
	if strings.TrimSpace(scope) == "" {
		return nil, nil
	}
	path := filepath.Join(scope, snapshotFileName)
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read tool snapshot: %w", err)
	}
	if info.Size() > snapshotByteLimit {
		return nil, fmt.Errorf("tool snapshot exceeds %d bytes", snapshotByteLimit)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read tool snapshot: %w", err)
	}
	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, fmt.Errorf("decode tool snapshot: %w", err)
	}
	if err := snapshot.validate(); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func (s *Snapshot) validate() error {
	if s.Version != snapshotVersion {
		return fmt.Errorf("unsupported tool snapshot version %d", s.Version)
	}
	seen := make(map[string]bool, len(s.Servers))
	for serverIndex := range s.Servers {
		server := &s.Servers[serverIndex]
		if !serverNamePattern.MatchString(server.Name) {
			return fmt.Errorf("tool snapshot has invalid server name %q", server.Name)
		}
		if seen[server.Name] {
			return fmt.Errorf("tool snapshot has duplicate server %q", server.Name)
		}
		seen[server.Name] = true
		for toolIndex := range server.Tools {
			tool := &server.Tools[toolIndex]
			if !modelToolNamePattern.MatchString(tool.ModelName) {
				return fmt.Errorf("tool snapshot has invalid tool name %q", tool.ModelName)
			}
			if strings.TrimSpace(tool.WireName) == "" {
				return fmt.Errorf("tool snapshot has an empty wire name for %q", tool.ModelName)
			}
			var document map[string]any
			if err := json.Unmarshal(tool.Schema, &document); err != nil || document == nil {
				return fmt.Errorf("tool snapshot has an invalid schema for %q", tool.ModelName)
			}
		}
	}
	return nil
}

// saveSnapshot atomically writes the discovery result with owner-only
// permissions.
func saveSnapshot(scope string, snapshot *Snapshot) error {
	if strings.TrimSpace(scope) == "" {
		return errors.New("state scope is required")
	}
	if err := os.MkdirAll(scope, 0o700); err != nil {
		return fmt.Errorf("create state scope: %w", err)
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("encode tool snapshot: %w", err)
	}
	if len(data) > snapshotByteLimit {
		return fmt.Errorf("tool snapshot exceeds %d bytes", snapshotByteLimit)
	}
	temporary, err := os.CreateTemp(scope, snapshotFileName+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary tool snapshot: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("secure temporary tool snapshot: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary tool snapshot: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync temporary tool snapshot: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary tool snapshot: %w", err)
	}
	if err := os.Rename(temporaryPath, filepath.Join(scope, snapshotFileName)); err != nil {
		return fmt.Errorf("replace tool snapshot: %w", err)
	}
	return nil
}

func emptySnapshot(configHash string) *Snapshot {
	return &Snapshot{
		Version:     snapshotVersion,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano),
		ConfigHash:  configHash,
		Servers:     []ServerSnapshot{},
	}
}

// toolNames returns the exported model tool names in snapshot order.
func (s *Snapshot) toolNames() []string {
	if s == nil {
		return nil
	}
	var names []string
	for _, server := range s.Servers {
		for _, tool := range server.Tools {
			names = append(names, tool.ModelName)
		}
	}
	return names
}

// server returns the snapshot of one server.
func (s *Snapshot) server(name string) (ServerSnapshot, bool) {
	if s == nil {
		return ServerSnapshot{}, false
	}
	for _, server := range s.Servers {
		if server.Name == name {
			return server, true
		}
	}
	return ServerSnapshot{}, false
}

// diffToolNames compares two snapshots and returns added and removed names.
func diffToolNames(previous, next *Snapshot) (added, removed []string) {
	previousNames := make(map[string]bool)
	for _, name := range previous.toolNames() {
		previousNames[name] = true
	}
	nextNames := make(map[string]bool)
	for _, name := range next.toolNames() {
		nextNames[name] = true
		if !previousNames[name] {
			added = append(added, name)
		}
	}
	for _, name := range previous.toolNames() {
		if !nextNames[name] {
			removed = append(removed, name)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}
