package toolmcp

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/tool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const truncationNotice = "\n\n[tool.mcp: result truncated; the configured size limit was reached]"

// mapCallResult converts an MCP call result into authoritative tool content.
// A server-reported tool error is a business outcome and is expressed inside
// the content, not as a Go error, so the model can see and correct it.
func mapCallResult(server string, result *mcp.CallToolResult, maxBytes int) tool.Result {
	if result == nil {
		return tool.Result{Content: content.FromText("tool.mcp: MCP server returned no result")}
	}
	if maxBytes < 1 {
		maxBytes = defaultMaxResultBytes
	}

	parts := make(content.Content, 0, len(result.Content)+2)
	remaining := maxBytes
	truncated := false
	lastWasText := false

	addText := func(value string) {
		if value == "" {
			return
		}
		if lastWasText {
			value = "\n" + value
		}
		limited, used, cut := truncateText(value, remaining)
		if limited != "" {
			parts = append(parts, content.Text(limited))
			remaining -= used
		}
		truncated = truncated || cut
		lastWasText = true
	}

	if result.IsError {
		parts = append(parts, content.Text(fmt.Sprintf("[tool.mcp: server %q reported a tool error]", server)))
	}

	for _, item := range result.Content {
		if remaining <= 0 {
			truncated = true
			break
		}
		switch value := item.(type) {
		case *mcp.TextContent:
			addText(value.Text)
		case *mcp.ImageContent:
			part, used, ok, overBudget := mediaPart(content.KindImage, value.MIMEType, "image", value.Data, remaining)
			if ok {
				parts = append(parts, part)
				remaining -= used
				lastWasText = false
				continue
			}
			if overBudget {
				truncated = true
			} else {
				addText("[tool.mcp: omitted image content that could not be mapped]")
			}
		case *mcp.AudioContent:
			part, used, ok, overBudget := mediaPart(content.KindAudio, value.MIMEType, "audio", value.Data, remaining)
			if ok {
				parts = append(parts, part)
				remaining -= used
				lastWasText = false
				continue
			}
			if overBudget {
				truncated = true
			} else {
				addText("[tool.mcp: omitted audio content that could not be mapped]")
			}
		case *mcp.ResourceLink:
			name := value.Name
			if name == "" {
				name = value.URI
			}
			parts = append(parts, content.URI(content.KindFile, value.MIMEType, name, value.URI))
			remaining -= len(value.URI)
			lastWasText = false
		case *mcp.EmbeddedResource:
			if value.Resource == nil {
				continue
			}
			if value.Resource.Text != "" {
				addText(value.Resource.Text)
				continue
			}
			kind := mediaKind(value.Resource.MIMEType)
			name := value.Resource.URI
			if name == "" {
				name = "resource"
			}
			part, used, ok, overBudget := mediaPart(kind, value.Resource.MIMEType, name, value.Resource.Blob, remaining)
			if ok {
				parts = append(parts, part)
				remaining -= used
				lastWasText = false
				continue
			}
			if overBudget {
				truncated = true
			} else {
				addText("[tool.mcp: omitted embedded resource that could not be mapped]")
			}
		default:
			// The SDK cannot produce unknown content types, but a future
			// protocol revision may. Degrade to an explicit placeholder
			// instead of dropping information silently.
			addText("[tool.mcp: unsupported content type returned by the MCP server]")
		}
	}

	if result.StructuredContent != nil {
		encoded, err := json.Marshal(result.StructuredContent)
		if err == nil && len(encoded) > 0 {
			addText("[tool.mcp: structured content]\n" + strings.TrimSpace(string(encoded)))
		}
	}

	if len(parts) == 0 {
		parts = append(parts, content.Text("[tool.mcp: the MCP server returned no content]"))
	}
	if truncated {
		parts = append(parts, content.Text(truncationNotice))
	}
	return tool.Result{Content: parts}
}

// truncateText bounds text at a UTF-8 boundary and reports the consumed bytes.
func truncateText(value string, limit int) (string, int, bool) {
	if limit <= 0 {
		return "", 0, value != ""
	}
	if len(value) <= limit {
		return value, len(value), false
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	if cut == 0 {
		return "", 0, true
	}
	return value[:cut], cut, true
}

// mediaPart decodes one base64 media payload and bounds it against the
// remaining result budget.
func mediaPart(kind content.Kind, mimeType, name string, encoded []byte, limit int) (content.Part, int, bool, bool) {
	if strings.TrimSpace(mimeType) == "" {
		return content.Part{}, 0, false, false
	}
	decoded, err := decodeBase64(encoded)
	if err != nil || len(decoded) == 0 {
		return content.Part{}, 0, false, false
	}
	if len(decoded) > limit {
		return content.Part{}, 0, false, true
	}
	return content.Inline(kind, mimeType, name, decoded), len(decoded), true, false
}

func decodeBase64(encoded []byte) ([]byte, error) {
	text := strings.TrimSpace(string(encoded))
	if decoded, err := base64.StdEncoding.DecodeString(text); err == nil {
		return decoded, nil
	}
	return base64.RawStdEncoding.DecodeString(text)
}

// mediaKind selects a content kind from a MIME type.
func mediaKind(mimeType string) content.Kind {
	lower := strings.ToLower(strings.TrimSpace(mimeType))
	switch {
	case strings.HasPrefix(lower, "image/"):
		return content.KindImage
	case strings.HasPrefix(lower, "audio/"):
		return content.KindAudio
	case strings.HasPrefix(lower, "video/"):
		return content.KindVideo
	default:
		return content.KindFile
	}
}
