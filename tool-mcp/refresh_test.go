package toolmcp

import "testing"

func TestToolPrefixHint(t *testing.T) {
	cases := []struct {
		tool ToolSnapshot
		want string
	}{
		{ToolSnapshot{ModelName: "mcp_github_create_issue", WireName: "create_issue"}, "mcp_github_*"},
		{ToolSnapshot{ModelName: "mcp_filesystem_read_file", WireName: "read file"}, "mcp_filesystem_*"},
	}
	for _, testCase := range cases {
		if got := toolPrefixHint(testCase.tool); got != testCase.want {
			t.Fatalf("toolPrefixHint(%q) = %q, want %q", testCase.tool.ModelName, got, testCase.want)
		}
	}
}

func TestApprovalHintsAreNeverNil(t *testing.T) {
	if hints := approvalHints(&Snapshot{}); hints == nil {
		t.Fatal("approval hints must serialize as an array")
	}
	if hints := approvalHints(&Snapshot{Servers: []ServerSnapshot{{Name: "empty"}}}); len(hints) != 0 {
		t.Fatalf("hints = %v", hints)
	}
}

func TestBuildServerReports(t *testing.T) {
	snapshot := &Snapshot{Servers: []ServerSnapshot{{
		Name: "demo", Transport: transportStdio, Status: serverStatusError,
		Error: "boom", ToolCount: 2, SchemaBytes: 64,
		Rejected: []RejectedTool{{WireName: "broken", Reason: "why"}},
	}}}
	reports := buildServerReports(snapshot)
	if len(reports) != 1 {
		t.Fatalf("reports = %#v", reports)
	}
	report := reports[0]
	if report.Tools != 2 || report.Rejected != 1 || report.SchemaBytes != 64 || report.Error != "boom" {
		t.Fatalf("report = %#v", report)
	}
}
