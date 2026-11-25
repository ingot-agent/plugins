package toolmcp

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// modelToolNamePattern is the tool-name grammar enforced by the standard tool
// Runtime. Every exported name must satisfy it or the Runtime Image fails to
// start.
var modelToolNamePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`)

const toolNameHashLength = 6

// sanitizeSegment reduces an arbitrary MCP identifier to the characters allowed
// by the tool Runtime name grammar.
func sanitizeSegment(value string) string {
	var builder strings.Builder
	lastSeparator := false
	for _, r := range strings.ToLower(value) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
			lastSeparator = false
			continue
		}
		if !lastSeparator && builder.Len() > 0 {
			builder.WriteByte('_')
			lastSeparator = true
		}
	}
	return strings.Trim(builder.String(), "_")
}

// buildToolName returns the model-visible name for one remote tool:
// <prefix>_<server>_<tool>, bounded to the common 64-byte provider limit.
func buildToolName(prefix, server, wireName string) string {
	segments := make([]string, 0, 3)
	for _, raw := range []string{prefix, server, wireName} {
		if segment := sanitizeSegment(raw); segment != "" {
			segments = append(segments, segment)
		}
	}
	name := strings.Join(segments, "_")
	if name == "" {
		name = "mcp_tool"
	}
	return truncateToolName(name)
}

// truncateToolName bounds a name while keeping a stable digest of the full
// name, so that two long names never collapse onto one identifier.
func truncateToolName(name string) string {
	if len(name) <= maxToolNameBytes {
		return name
	}
	digest := shortDigest(name)
	head := strings.TrimRight(name[:maxToolNameBytes-len(digest)-1], "_")
	if head == "" {
		return "tool_" + digest
	}
	return head + "_" + digest
}

// disambiguateToolName appends a stable digest to a name that collided with an
// already exported tool.
func disambiguateToolName(name, salt string) string {
	digest := shortDigest(salt)
	if len(name)+len(digest)+1 > maxToolNameBytes {
		name = truncateToolName(name)
		if len(name)+len(digest)+1 > maxToolNameBytes {
			name = strings.TrimRight(name[:maxToolNameBytes-len(digest)-1], "_")
		}
	}
	if name == "" {
		return "tool_" + digest
	}
	return name + "_" + digest
}

func shortDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:toolNameHashLength]
}
