package toolmcp

import (
	"regexp"
	"strings"
	"testing"
)

func TestSanitizeSegment(t *testing.T) {
	cases := map[string]string{
		"create_issue":   "create_issue",
		"Create Issue":   "create_issue",
		"a.b/c":          "a_b_c",
		"__x__":          "x",
		"!!!":            "",
		"Ünicode":        "nicode",
		"read-file.json": "read_file_json",
		"tool.with.dots": "tool_with_dots",
		"ALLCAPS":        "allcaps",
		"trailing---":    "trailing",
	}
	for input, want := range cases {
		if got := sanitizeSegment(input); got != want {
			t.Fatalf("sanitizeSegment(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestBuildToolNameMatchesRuntimePattern(t *testing.T) {
	pattern := regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`)
	names := []string{
		buildToolName("mcp", "github", "create_issue"),
		buildToolName("mcp", "7zip", "extract all"),
		buildToolName("mcp", "server", "!!!empty"),
		buildToolName("mcp", "very-long-server-name-that-goes-on", "and-a-very-long-tool-name-that-also-goes-on-and-on"),
		buildToolName("mcp", "a", strings.Repeat("x", 300)),
	}
	for _, name := range names {
		if !pattern.MatchString(name) {
			t.Fatalf("name %q does not satisfy the runtime pattern", name)
		}
		if len(name) > maxToolNameBytes {
			t.Fatalf("name %q is %d bytes, want at most %d", name, len(name), maxToolNameBytes)
		}
	}
}

func TestBuildToolNameCollapsesEmptySegments(t *testing.T) {
	if got := buildToolName("mcp", "srv", "!!!"); got != "mcp_srv" {
		t.Fatalf("buildToolName = %q, want mcp_srv", got)
	}
}

func TestTruncateToolNameIsStable(t *testing.T) {
	first := truncateToolName(strings.Repeat("a", 100))
	second := truncateToolName(strings.Repeat("a", 100))
	if first != second {
		t.Fatal("truncation must be deterministic")
	}
	if len(first) > maxToolNameBytes {
		t.Fatalf("truncated name is %d bytes", len(first))
	}
	other := truncateToolName(strings.Repeat("a", 99) + "b")
	if other == first {
		t.Fatal("different long names must not collapse onto one identifier")
	}
}

func TestDisambiguateToolName(t *testing.T) {
	base := buildToolName("mcp", "server", "tool")
	first := disambiguateToolName(base, "server:tool:0")
	second := disambiguateToolName(base, "server:tool:1")
	if first == second {
		t.Fatal("different salts must produce different names")
	}
	for _, name := range []string{first, second} {
		if len(name) > maxToolNameBytes {
			t.Fatalf("name %q is too long", name)
		}
		if !modelToolNamePattern.MatchString(name) {
			t.Fatalf("name %q does not satisfy the runtime pattern", name)
		}
	}
}

func TestAssignModelNameIsUnique(t *testing.T) {
	taken := map[string]bool{}
	first, ok := assignModelName("mcp", "server", "tool", taken)
	if !ok {
		t.Fatal("first assignment failed")
	}
	second, ok := assignModelName("mcp", "server", "tool", taken)
	if !ok {
		t.Fatal("second assignment failed")
	}
	if first == second {
		t.Fatal("assignments collided")
	}
	if !modelToolNamePattern.MatchString(second) {
		t.Fatalf("name %q does not satisfy the runtime pattern", second)
	}
}
