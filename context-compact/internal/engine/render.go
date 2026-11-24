package engine

import (
	"encoding/json"
	"strings"

	"github.com/ingot-agent/sdk/model"
)

// maxRenderedArguments bounds how much of one tool call's arguments appear in a
// summary prompt. A shell command can be tens of kilobytes of heredoc; only its
// shape and the files it touches matter for recall.
const maxRenderedArguments = 400

// maxRenderedOutput bounds the tool output excerpt kept for summarization.
const maxRenderedOutput = 1200

// CompactArguments renders tool call arguments for a summary prompt, keeping a
// prefix and collapsing whitespace so the result stays on one line.
func CompactArguments(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	text := collapseWhitespace(string(raw))
	if len(text) <= maxRenderedArguments {
		return text
	}
	return truncateBytes(text, maxRenderedArguments) + "…"
}

// SummarizeToolOutput renders a tool result for a summary prompt. The excerpt is
// two-sided because failures usually appear at the start and at the end of a
// command's output.
func SummarizeToolOutput(message model.Message) string {
	text := collapseWhitespace(TextOf(message))
	if text == "" {
		return "(no output)"
	}
	if len(text) <= maxRenderedOutput {
		return text
	}
	half := maxRenderedOutput / 2
	return truncateBytes(text, half) + " … " + tailBytes(text, half)
}

// collapseWhitespace replaces every run of whitespace with one space.
func collapseWhitespace(text string) string {
	var builder strings.Builder
	builder.Grow(len(text))
	space := false
	for _, r := range text {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			space = true
			continue
		}
		if space && builder.Len() > 0 {
			builder.WriteByte(' ')
		}
		space = false
		builder.WriteRune(r)
	}
	return builder.String()
}

func truncateBytes(text string, size int) string {
	if len(text) <= size {
		return text
	}
	cut := size
	for cut > 0 && !isRuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

func tailBytes(text string, size int) string {
	if len(text) <= size {
		return text
	}
	start := len(text) - size
	for start < len(text) && !isRuneStart(text[start]) {
		start++
	}
	return text[start:]
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
