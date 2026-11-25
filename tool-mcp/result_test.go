package toolmcp

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/ingot-agent/sdk/content"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func resultText(t *testing.T, result mcp.CallToolResult, maxBytes int) string {
	t.Helper()
	mapped := mapCallResult("demo", &result, maxBytes)
	text, ok := content.TextOnly(mapped.Content)
	if !ok {
		t.Fatalf("result is not text only: %#v", mapped.Content)
	}
	return text
}

func TestMapCallResultText(t *testing.T) {
	text := resultText(t, mcp.CallToolResult{Content: []mcp.Content{
		&mcp.TextContent{Text: "first"},
		&mcp.TextContent{Text: "second"},
	}}, 1024)
	if text != "first\nsecond" {
		t.Fatalf("text = %q", text)
	}
}

func TestMapCallResultMarksToolError(t *testing.T) {
	text := resultText(t, mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: "boom"}},
	}, 1024)
	if !strings.Contains(text, "reported a tool error") {
		t.Fatalf("error marker missing: %q", text)
	}
	if !strings.Contains(text, "boom") {
		t.Fatalf("server message missing: %q", text)
	}
}

func TestMapCallResultDecodesImage(t *testing.T) {
	payload := []byte("fake-png-bytes")
	encoded := base64.StdEncoding.EncodeToString(payload)
	result := mcp.CallToolResult{Content: []mcp.Content{
		&mcp.ImageContent{MIMEType: "image/png", Data: []byte(encoded)},
	}}
	mapped := mapCallResult("demo", &result, 1024)
	if len(mapped.Content) != 1 {
		t.Fatalf("parts = %d", len(mapped.Content))
	}
	part := mapped.Content[0]
	if part.Kind != content.KindImage {
		t.Fatalf("kind = %d", part.Kind)
	}
	if string(part.Media.Source.Data) != string(payload) {
		t.Fatalf("data = %q, want %q", part.Media.Source.Data, payload)
	}
	if part.Media.MIMEType != "image/png" {
		t.Fatalf("mime = %q", part.Media.MIMEType)
	}
}

func TestMapCallResultDropsOversizedMedia(t *testing.T) {
	payload := []byte(strings.Repeat("x", 2048))
	encoded := base64.StdEncoding.EncodeToString(payload)
	result := mcp.CallToolResult{Content: []mcp.Content{
		&mcp.ImageContent{MIMEType: "image/png", Data: []byte(encoded)},
	}}
	mapped := mapCallResult("demo", &result, 128)
	text, ok := content.TextOnly(mapped.Content)
	if !ok {
		t.Fatalf("oversized media should degrade to text: %#v", mapped.Content)
	}
	if !strings.Contains(text, "truncated") {
		t.Fatalf("truncation notice missing: %q", text)
	}
}

func TestMapCallResultResourceLink(t *testing.T) {
	result := mcp.CallToolResult{Content: []mcp.Content{
		&mcp.ResourceLink{URI: "file:///tmp/a.txt", Name: "a.txt", MIMEType: "text/plain"},
	}}
	mapped := mapCallResult("demo", &result, 1024)
	part := mapped.Content[0]
	if part.Kind != content.KindFile {
		t.Fatalf("kind = %d", part.Kind)
	}
	if part.Media.Source.URI != "file:///tmp/a.txt" {
		t.Fatalf("uri = %q", part.Media.Source.URI)
	}
}

func TestMapCallResultEmbeddedText(t *testing.T) {
	result := mcp.CallToolResult{Content: []mcp.Content{
		&mcp.EmbeddedResource{Resource: &mcp.ResourceContents{URI: "mem://a", Text: "embedded"}},
	}}
	if text := resultText(t, result, 1024); text != "embedded" {
		t.Fatalf("text = %q", text)
	}
}

func TestMapCallResultStructuredContent(t *testing.T) {
	result := mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: "ok"}},
		StructuredContent: map[string]any{"count": 2},
	}
	text := resultText(t, result, 1024)
	if !strings.Contains(text, "structured content") || !strings.Contains(text, `"count":2`) {
		t.Fatalf("structured content missing: %q", text)
	}
}

func TestMapCallResultTruncatesText(t *testing.T) {
	result := mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strings.Repeat("a", 100)}}}
	text := resultText(t, result, 10)
	if !strings.HasPrefix(text, strings.Repeat("a", 10)) {
		t.Fatalf("text = %q", text)
	}
	if !strings.Contains(text, "truncated") {
		t.Fatalf("truncation notice missing: %q", text)
	}
}

func TestMapCallResultEmptyContent(t *testing.T) {
	if text := resultText(t, mcp.CallToolResult{}, 1024); !strings.Contains(text, "no content") {
		t.Fatalf("text = %q", text)
	}
}

func TestTruncateTextKeepsUTF8Boundary(t *testing.T) {
	value := "汉字"
	limited, used, cut := truncateText(value, 4)
	if !cut || used != 3 || limited != "汉" {
		t.Fatalf("limited=%q used=%d cut=%v", limited, used, cut)
	}
}
