package sessionsqlite

import (
	"context"
	"fmt"
	"math"
	"unicode/utf8"

	"github.com/ingot-agent/sdk/session"
)

func (s *store) AddTotalTokens(ctx context.Context, targets []session.ID, delta int64) ([]session.Metadata, error) {
	if ctx == nil {
		return nil, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(targets) == 0 || delta < 0 {
		return nil, fmt.Errorf("token settlement requires targets and a nonnegative delta")
	}
	ids := make([]session.ID, 0, len(targets))
	seen := make(map[session.ID]bool, len(targets))
	for _, id := range targets {
		if id == "" || !utf8.ValidString(string(id)) {
			return nil, fmt.Errorf("token settlement requires valid session identities")
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	metadata := make([]session.Metadata, len(ids))
	for i, id := range ids {
		metadata[i], err = metadataByID(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if metadata[i].TotalToken > math.MaxInt64-delta {
			return nil, fmt.Errorf("session %q token total overflows int64", id)
		}
	}
	for i, id := range ids {
		if _, err := tx.ExecContext(ctx, "UPDATE sessions SET totaltoken = totaltoken + ? WHERE id = ?", delta, string(id)); err != nil {
			return nil, fmt.Errorf("settle tokens for session %q: %w", id, err)
		}
		metadata[i].TotalToken += delta
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit token settlement: %w", err)
	}
	return metadata, nil
}

var _ session.TokenUsageStore = (*store)(nil)
