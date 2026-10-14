package openairesponses

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"

	"github.com/ingot-agent/sdk/observation"
)

// A session keeps the same cache partition across rounds, retries, turns and
// process restarts. Round/turn IDs would defeat reuse. Hash the opaque session
// identity so neither the body nor headers expose caller-defined identifiers.
func sessionCacheKey(ctx context.Context) string {
	correlation, ok := observation.CorrelationFromContext(ctx)
	if !ok || correlation.SessionID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("ingot.responses.session\x00" + string(correlation.SessionID)))
	return hex.EncodeToString(sum[:])
}

func addSessionHeaders(ctx context.Context, headers http.Header) {
	key := sessionCacheKey(ctx)
	if key == "" {
		return
	}
	// These are the Responses session headers used by Codex. Compatible
	// gateways can use them for routing even when the model handles caching
	// automatically. Explicit provider headers retain their configured values.
	for _, name := range []string{"session-id", "thread-id"} {
		if _, configured := headers[http.CanonicalHeaderKey(name)]; !configured {
			headers.Set(name, key)
		}
	}
}
