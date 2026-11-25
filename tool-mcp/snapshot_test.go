package toolmcp

import (
	"encoding/json"
	"testing"
)

func sampleSnapshot() *Snapshot {
	return &Snapshot{
		Version:     snapshotVersion,
		GeneratedAt: "2026-01-01T00:00:00Z",
		ConfigHash:  "abc",
		Servers: []ServerSnapshot{{
			Name:      "demo",
			Transport: transportStdio,
			Status:    serverStatusOK,
			ToolCount: 2,
			Tools: []ToolSnapshot{
				{ModelName: "mcp_demo_echo", WireName: "echo", Description: "echo", Schema: json.RawMessage(`{"type":"object"}`)},
				{ModelName: "mcp_demo_list", WireName: "list", Description: "list", Schema: json.RawMessage(`{"type":"object"}`)},
			},
			Rejected: []RejectedTool{{WireName: "broken", Reason: "compile schema"}},
		}},
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := saveSnapshot(dir, sampleSnapshot()); err != nil {
		t.Fatalf("saveSnapshot: %v", err)
	}
	loaded, err := loadSnapshot(dir)
	if err != nil {
		t.Fatalf("loadSnapshot: %v", err)
	}
	if loaded.ConfigHash != "abc" || len(loaded.Servers) != 1 {
		t.Fatalf("round trip lost data: %#v", loaded)
	}
	if len(loaded.Servers[0].Tools) != 2 {
		t.Fatalf("tools = %d", len(loaded.Servers[0].Tools))
	}
}

func TestLoadSnapshotMissingIsNotAnError(t *testing.T) {
	snapshot, err := loadSnapshot(t.TempDir())
	if err != nil {
		t.Fatalf("loadSnapshot: %v", err)
	}
	if snapshot != nil {
		t.Fatal("expected a nil snapshot")
	}
}

func TestLoadSnapshotRejectsCorruptData(t *testing.T) {
	cases := map[string]string{
		"invalid json":     `{`,
		"bad version":      `{"snapshot_version":9}`,
		"bad tool name":    `{"snapshot_version":1,"servers":[{"name":"demo","tools":[{"model_name":"Bad Name","wire_name":"x","schema":{"type":"object"}}]}]}`,
		"missing schema":   `{"snapshot_version":1,"servers":[{"name":"demo","tools":[{"model_name":"mcp_demo_x","wire_name":"x"}]}]}`,
		"duplicate server": `{"snapshot_version":1,"servers":[{"name":"demo"},{"name":"demo"}]}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, snapshotFileName, content)
			if _, err := loadSnapshot(dir); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestDiffToolNames(t *testing.T) {
	previous := &Snapshot{Servers: []ServerSnapshot{{
		Tools: []ToolSnapshot{{ModelName: "a"}, {ModelName: "b"}},
	}}}
	next := &Snapshot{Servers: []ServerSnapshot{{
		Tools: []ToolSnapshot{{ModelName: "b"}, {ModelName: "c"}},
	}}}
	added, removed := diffToolNames(previous, next)
	if len(added) != 1 || added[0] != "c" {
		t.Fatalf("added = %v", added)
	}
	if len(removed) != 1 || removed[0] != "a" {
		t.Fatalf("removed = %v", removed)
	}
}

func TestNilSnapshotHasNoTools(t *testing.T) {
	var snapshot *Snapshot
	if names := snapshot.toolNames(); names != nil {
		t.Fatalf("names = %v", names)
	}
}
