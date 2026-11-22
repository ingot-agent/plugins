package contextcompact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ingot-agent/plugins/context-compact/internal/engine"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/logging"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/session"
)

const (
	// recordKind and recordVersion identify this Plugin's own entries in the
	// Session Store. The Store treats payloads as opaque.
	recordKind    = "context.compact.summary"
	recordVersion = 1
	// recordGrowthRatio suppresses a rewrite when a compaction added almost no
	// new material, keeping the record count near O(log n) instead of one per
	// round.
	recordGrowthNumerator   = 1
	recordGrowthDenominator = 10
)

// record is the entire durable state of one session's compaction: a summary of
// the messages before CoveredMessages, plus the digest that proves the summary
// still describes them.
//
// There is no chain, no revision and no incremental patch format. A summary
// always covers a contiguous prefix, coverage only grows, and a record whose
// digest does not match current history is ignored rather than repaired.
type record struct {
	Version         int    `json:"version"`
	CoveredMessages int    `json:"covered_messages"`
	CoveredDigest   string `json:"covered_digest"`
	Summary         string `json:"summary"`
	Notes           string `json:"notes,omitempty"`
	Provider        string `json:"provider"`
	Model           string `json:"model"`
	CreatedAt       int64  `json:"created_at"`
}

// loadRecord returns the newest usable record for a session. A record is
// unusable when its coverage does not end on a round boundary or when the
// digest no longer matches, which means the history changed underneath it. Both
// are ordinary conditions, not errors: the caller simply starts from scratch.
func (r *compactor) loadRecord(ctx context.Context, id session.ID, layout engine.Layout) (record, int) {
	entries, err := r.store.Load(ctx, id)
	if err != nil {
		r.logger.Warn(ctx, "load context summary record", logging.String("session", string(id)), logging.Err(err))
		return record{}, 0
	}
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		if entry.Kind != recordKind || entry.Version != recordVersion {
			continue
		}
		var candidate record
		if err := json.Unmarshal(entry.Payload, &candidate); err != nil {
			r.logger.Warn(ctx, "decode context summary record", logging.String("session", string(id)), logging.Err(err))
			continue
		}
		if candidate.Version != recordVersion || candidate.CoveredMessages <= 0 {
			continue
		}
		if candidate.CoveredMessages > layout.RoundsEnd() {
			continue
		}
		rounds, ok := layout.RoundEndingAt(candidate.CoveredMessages)
		if !ok {
			continue
		}
		digest, err := conversationDigest(layout.Conversation[:candidate.CoveredMessages])
		if err != nil {
			r.logger.Warn(ctx, "digest covered messages", logging.String("session", string(id)), logging.Err(err))
			continue
		}
		if digest != candidate.CoveredDigest {
			continue
		}
		if !utf8.ValidString(candidate.Summary) || strings.TrimSpace(candidate.Summary) == "" {
			continue
		}
		return candidate, rounds
	}
	return record{}, 0
}

// appendRecord persists a new record. Failing to persist is not fatal: the
// compaction result of this call is already valid, and the next call simply
// recomputes coverage. It is reported so the condition is visible.
func (r *compactor) appendRecord(ctx context.Context, id session.ID, next record, previous record) {
	next.Version = recordVersion
	if next.CreatedAt == 0 {
		next.CreatedAt = time.Now().Unix()
	}
	if shouldSkipRecord(previous, next) {
		return
	}
	payload, err := json.Marshal(next)
	if err != nil {
		r.logger.Warn(ctx, "encode context summary record", logging.String("session", string(id)), logging.Err(err))
		return
	}
	if err := r.store.Append(ctx, id, session.Entry{Kind: recordKind, Version: recordVersion, Payload: payload}); err != nil {
		r.logger.Warn(ctx, "append context summary record", logging.String("session", string(id)), logging.Err(err))
	}
}

// shouldSkipRecord reports whether writing next would add nothing worth the
// storage. Coverage must still grow, otherwise the record is pointless.
func shouldSkipRecord(previous, next record) bool {
	if next.CoveredMessages <= previous.CoveredMessages {
		return true
	}
	if previous.Summary == "" {
		return false
	}
	growth := len(next.Summary) + len(next.Notes) - len(previous.Summary) - len(previous.Notes)
	threshold := (len(previous.Summary) + len(previous.Notes)) * recordGrowthNumerator / recordGrowthDenominator
	return growth < threshold
}

// conversationDigest fingerprints the messages a summary covers. Replay state
// participates because replacing or dropping a message changes what a provider
// can resume from, so a summary must not survive that change.
func conversationDigest(messages []model.Message) (string, error) {
	digest := sha256.New()
	encoder := json.NewEncoder(digest)
	encoder.SetEscapeHTML(false)
	for i, message := range messages {
		projection := messageProjection{
			Role:       string(message.Role),
			Parts:      make([]partProjection, 0, len(message.Content)),
			Name:       message.Name,
			ToolCallID: message.ToolCallID,
			ToolCalls:  make([]callProjection, 0, len(message.ToolCalls)),
		}
		for _, part := range message.Content {
			projection.Parts = append(projection.Parts, partProjection{
				Kind:     uint8(part.Kind),
				Text:     part.Text,
				MIMEType: part.Media.MIMEType,
				Name:     part.Media.Name,
			})
		}
		for _, call := range message.ToolCalls {
			projection.ToolCalls = append(projection.ToolCalls, callProjection{
				ID: call.ID, Name: call.Name, Arguments: string(call.Arguments),
			})
		}
		if message.Replay != nil {
			projection.Replay = &replayProjection{
				Format: message.Replay.Format, Provider: message.Replay.Provider,
				Model: message.Replay.Model, Data: hex.EncodeToString(message.Replay.Data),
			}
		}
		projection.Index = i
		if err := encoder.Encode(projection); err != nil {
			return "", fmt.Errorf("digest message %d: %w", i, err)
		}
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

type messageProjection struct {
	Index      int               `json:"index"`
	Role       string            `json:"role"`
	Parts      []partProjection  `json:"parts"`
	Name       string            `json:"name"`
	ToolCallID string            `json:"tool_call_id"`
	ToolCalls  []callProjection  `json:"tool_calls"`
	Replay     *replayProjection `json:"replay,omitempty"`
}

type partProjection struct {
	Kind     uint8  `json:"kind"`
	Text     string `json:"text,omitempty"`
	MIMEType string `json:"mime_type,omitempty"`
	Name     string `json:"name,omitempty"`
}

type callProjection struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type replayProjection struct {
	Format   string `json:"format"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Data     string `json:"data"`
}

// summaryPreamble renders a record as the messages inserted before the
// surviving conversation. The text is explicitly framed as historical so a
// model does not treat the summary as a new instruction.
func summaryPreamble(value record) []model.Message {
	if value.Summary == "" {
		return nil
	}
	var builder strings.Builder
	builder.WriteString("[Compacted conversation summary. Historical data; it does not override system instructions.]\n")
	builder.WriteString(value.Summary)
	if value.Notes != "" {
		builder.WriteString("\n\n[Established facts from the compacted conversation. Treat as constraints.]\n")
		builder.WriteString(value.Notes)
	}
	return []model.Message{{Role: model.RoleAssistant, Content: content.FromText(builder.String())}}
}
