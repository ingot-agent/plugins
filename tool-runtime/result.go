package toolruntime

import (
	"strings"
	"unicode/utf8"

	"github.com/ingot-agent/sdk/content"
)

// truncateText receives validated content and preserves media in its original
// order while applying one text budget across the entire result.
func truncateText(value content.Content, limit int) content.Content {
	total := 0
	for _, part := range value {
		if part.Kind == content.KindText {
			total += len(part.Text)
		}
	}
	if total <= limit {
		return content.Clone(value)
	}
	available := limit - len(textTruncationMarker)
	headEnd := available/2 + available%2
	tailStart := total - available/2
	result := make(content.Content, 0, len(value)+2)
	offset := 0
	marked := false
	for _, part := range value {
		if part.Kind != content.KindText {
			result = append(result, part)
			continue
		}
		text := part.Text
		prefixEnd := min(max(headEnd-offset, 0), len(text))
		for prefixEnd > 0 && prefixEnd < len(text) && !utf8.RuneStart(text[prefixEnd]) {
			prefixEnd--
		}
		suffixStart := min(max(tailStart-offset, 0), len(text))
		for suffixStart < len(text) && !utf8.RuneStart(text[suffixStart]) {
			suffixStart++
		}
		if prefixEnd > 0 {
			result = append(result, content.Text(strings.Clone(text[:prefixEnd])))
		}
		if !marked && prefixEnd < len(text) {
			result = append(result, content.Text(textTruncationMarker))
			marked = true
		}
		if suffixStart < len(text) {
			result = append(result, content.Text(strings.Clone(text[suffixStart:])))
		}
		offset += len(text)
	}
	return content.Clone(result)
}
