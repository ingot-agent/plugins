package toolweb

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	// Tool runtime output is typically capped at 64 KiB. These limits keep an
	// individual tool result bounded even when the host raises that cap.
	maxFormattedOutputBytes = 512 << 10
	maxFetchContentBytes    = 512 << 10
	maxSearchContentBytes   = 16 << 10
	maxInlineFieldBytes     = 2048
)

func formatSearchResults(query string, results []searchResult, includeContent bool) string {
	var output strings.Builder
	if len(results) == 0 {
		fmt.Fprintf(&output, "No search results for %q.", query)
		return output.String()
	}
	fmt.Fprintf(&output, "Search results for %q: %d\n", query, len(results))
	output.Grow(min(len(results)*1024, maxFormattedOutputBytes))
	for index, result := range results {
		title := cleanInline(result.Title)
		if title == "" {
			title = "(untitled)"
		}
		fmt.Fprintf(&output, "\n%d. %s\n", index+1, title)
		url := cleanInline(result.URL)
		if url == "" {
			url = "(unknown)"
		}
		fmt.Fprintf(&output, "   URL: %s\n", url)

		metadata := make([]string, 0, 4)
		if site := stringValue(result.SiteName); site != "" {
			metadata = append(metadata, "Site: "+cleanInline(site))
		}
		if date := stringValue(result.Date); date != "" {
			metadata = append(metadata, "Published: "+cleanInline(date))
		}
		if result.Authority >= 0 && result.Authority <= 4 {
			metadata = append(metadata, fmt.Sprintf("Authority: %d", result.Authority))
		}
		metadata = append(metadata, fmt.Sprintf("Relevance: %.2f", result.RerankScore))
		fmt.Fprintf(&output, "   %s\n", strings.Join(metadata, " | "))

		summary := result.Summary
		if strings.TrimSpace(summary) == "" {
			summary = result.Snippet
		}
		if strings.TrimSpace(summary) != "" {
			fmt.Fprintf(&output, "   Summary: %s\n", cleanInline(summary))
		}
		if includeContent && strings.TrimSpace(result.Content) != "" {
			content := truncateUTF8(result.Content, maxSearchContentBytes, "\n   [content truncated; call web_fetch for the full page]")
			fmt.Fprintf(&output, "   Content:\n%s\n", content)
		}
	}
	return truncateUTF8(output.String(), maxFormattedOutputBytes, "\n[output truncated; narrow the query, reduce count, or set need_content=false]")
}

func formatFetchResult(title, target, content string) string {
	var output strings.Builder
	title = cleanInline(title)
	if title != "" {
		fmt.Fprintf(&output, "Title: %s\n", title)
	}
	fmt.Fprintf(&output, "URL: %s\n\n", cleanInline(target))
	output.WriteString(truncateUTF8(content, maxFetchContentBytes, "\n\n[content truncated]"))
	return output.String()
}

// cleanInline makes a server-provided single-line field safe to embed in tool
// output without allowing it to forge additional result lines.
func cleanInline(value string) string {
	value = strings.ToValidUTF8(value, "\uFFFD")
	value = truncateUTF8(value, maxInlineFieldBytes, "...")
	return strings.Join(strings.Fields(value), " ")
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// truncateUTF8 cuts value to at most limit bytes, backs up to a rune boundary,
// and appends marker. The marker must be shorter than limit.
func truncateUTF8(value string, limit int, marker string) string {
	if len(value) <= limit {
		return value
	}
	if limit <= len(marker) {
		return marker[:limit]
	}
	cut := limit - len(marker)
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut] + marker
}
