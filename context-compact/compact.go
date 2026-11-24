package contextcompact

import (
	"context"
	"fmt"
	"time"

	"github.com/ingot-agent/plugins/context-compact/internal/engine"
	"github.com/ingot-agent/sdk/contextwindow"
	"github.com/ingot-agent/sdk/logging"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/observation"
	"github.com/ingot-agent/sdk/usage"
)

// selectionLabel is used for telemetry when the request carries no explicit
// provider or model yet.
const unselectedLabel = "(unresolved)"

var _ contextwindow.Compactor = (*compactor)(nil)

// Compact prepares a bounded message context for one model invocation.
//
// The flow is linear and has exactly one error path, for a structurally invalid
// history. Everything else degrades:
//
//  1. measure the request and apply the previous summary, if it still matches
//  2. shrink deterministically until it fits (elision, then drops)
//  3. summarize whatever step 2 dropped, best effort
//  4. persist the new summary and publish an estimate
//
// Step 3 and 4 can fail without affecting the result of step 2, which is why a
// summarizer outage costs recall rather than the whole turn.
func (r *compactor) Compact(ctx context.Context, input contextwindow.CompactionRequest) (contextwindow.CompactionResult, error) {
	if ctx == nil {
		return contextwindow.CompactionResult{}, fmt.Errorf("nil context: %w", ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return contextwindow.CompactionResult{}, err
	}
	cfg := r.loader.load()

	layout, err := engine.Inspect(input.Invocation)
	if err != nil {
		return contextwindow.CompactionResult{}, err
	}

	release, err := r.gates.acquire(ctx, string(input.SessionID))
	if err != nil {
		return contextwindow.CompactionResult{}, err
	}
	defer release()

	previous, coveredRounds := r.loadRecord(ctx, input.SessionID, layout)
	started := time.Now()

	materialized := r.materialize(layout, previous, coveredRounds)
	before := engine.TokensOf(materialized) + layout.ToolsTokens
	result := compactionResult{
		messages: materialized,
		tokens:   before,
		coverage: coveredRounds,
	}
	// A session with a summary has already been compacted, so it stays compact:
	// recomputing elision from scratch every round would otherwise let the
	// request balloon back to its full size as soon as it dips below the
	// trigger, and the next round would shrink it again.
	if before > cfg.triggerTokens || coveredRounds > 0 {
		result = r.compactToTarget(ctx, input, layout, previous, coveredRounds, cfg)
	}

	r.publishContext(ctx, input.SessionID, result.tokens, r.countResult(input, layout, cfg))
	r.logCompaction(ctx, input, cfg, result, before, time.Since(started))
	return contextwindow.CompactionResult{Messages: result.messages, Changed: result.changed}, nil
}

// compressionResult is the internal outcome of one Compact call.
type compactionResult struct {
	messages []model.Message
	tokens   int64
	coverage int
	dropped  int
	elided   int
	changed  bool
	summary  summaryOutcome
	degraded string
}

// materialize builds the request with the summary replacing the rounds it
// covers.
func (r *compactor) materialize(layout engine.Layout, previous record, coveredRounds int) []model.Message {
	sub := layout.FromRound(coveredRounds)
	messages := make([]model.Message, 0, len(layout.System)+1+len(layout.Conversation))
	messages = append(messages, layout.System...)
	messages = append(messages, summaryPreamble(previous)...)
	for _, round := range sub.Rounds {
		messages = append(messages, sub.Messages(round)...)
	}
	messages = append(messages, sub.Tail()...)
	return messages
}

// compactToTarget applies the deterministic ladder, then summarizes whatever it
// had to remove.
func (r *compactor) compactToTarget(
	ctx context.Context,
	input contextwindow.CompactionRequest,
	layout engine.Layout,
	previous record,
	coveredRounds int,
	cfg normalizedConfig,
) compactionResult {
	sub := layout.FromRound(coveredRounds)
	preamble := summaryPreamble(previous)
	shrink := engine.Shrink(sub, engine.Config{
		TargetTokens:        cfg.targetTokens,
		ProtectRecentTokens: cfg.protectRecentTokens,
		Preamble:            preamble,
	})

	result := compactionResult{
		messages: shrink.Messages,
		tokens:   shrink.Tokens + layout.ToolsTokens,
		coverage: coveredRounds,
		dropped:  len(shrink.Dropped),
		elided:   shrink.Elided,
		changed:  len(shrink.Dropped) > 0 || shrink.Elided > 0 || coveredRounds > 0,
	}
	if shrink.Uncompactable {
		result.degraded = "uncompactable"
	}
	if len(shrink.Dropped) == 0 {
		return result
	}

	outcome := r.summarize(ctx, input, previous, shrink.Dropped, layout, cfg)
	result.summary = outcome
	if !outcome.OK {
		result.degraded = outcome.Reason
		return result
	}

	// Fold the new summary back in and shrink again: replacing the dropped
	// rounds with prose usually frees enough room to restore some rounds to a
	// shallower elision level. Coverage is committed only for rounds that were
	// actually summarized.
	// Coverage advances only over the rounds the summary actually describes.
	// When the request budget could not fit every dropped round, the rest stay
	// uncovered and are summarized by a later compaction, so no round is ever
	// marked as covered without appearing in a summary.
	nextCovered := coveredRounds + outcome.CoveredRounds
	if nextCovered > coveredRounds+len(shrink.Dropped) {
		nextCovered = coveredRounds + len(shrink.Dropped)
	}
	if nextCovered <= coveredRounds {
		result.degraded = "no_rounds_covered"
		return result
	}
	next := record{
		Version:         recordVersion,
		CoveredMessages: layout.Rounds[nextCovered-1].End,
		Summary:         outcome.Summary,
		Notes:           outcome.Notes,
		Provider:        outcome.Provider,
		Model:           outcome.Model,
	}
	digest, err := conversationDigest(layout.Conversation[:next.CoveredMessages])
	if err != nil {
		r.logger.Warn(ctx, "digest summarized messages", logging.String("session", string(input.SessionID)), logging.Err(err))
		return result
	}
	next.CoveredDigest = digest

	retry := engine.Shrink(layout.FromRound(nextCovered), engine.Config{
		TargetTokens:        cfg.targetTokens,
		ProtectRecentTokens: cfg.protectRecentTokens,
		Preamble:            summaryPreamble(next),
	})
	if retry.Tokens+layout.ToolsTokens <= cfg.targetTokens {
		result.messages = retry.Messages
		result.tokens = retry.Tokens + layout.ToolsTokens
		result.coverage = nextCovered
		result.dropped = len(retry.Dropped)
		result.elided = retry.Elided
		result.changed = true
		if len(retry.Dropped) > 0 {
			// The summary freed less room than the drops consumed. Those extra
			// rounds stay unsummarized for this call and are covered next time;
			// progress is monotone because drops are always oldest first.
			result.degraded = "partially_summarized"
		}
	}

	r.appendRecord(ctx, input.SessionID, next, previous)
	return result
}

// logCompaction records one structured line per compaction so a degradation is
// visible without reading provider errors.
func (r *compactor) logCompaction(
	ctx context.Context,
	input contextwindow.CompactionRequest,
	cfg normalizedConfig,
	result compactionResult,
	before int64,
	elapsed time.Duration,
) {
	attrs := []logging.Attr{
		logging.String("session", string(input.SessionID)),
		logging.Int("before_tokens", before),
		logging.Int("after_tokens", result.tokens),
		logging.Int("target_tokens", cfg.targetTokens),
		logging.Int("dropped_rounds", int64(result.dropped)),
		logging.Int("elided_rounds", int64(result.elided)),
		logging.Int("covered_rounds", int64(result.coverage)),
	}
	if result.summary.OK && result.summary.Summary != "" {
		attrs = append(attrs, logging.String("summary", "ok"))
	}
	if result.degraded != "" {
		attrs = append(attrs, logging.String("degraded", result.degraded))
	}
	if correlation, ok := observation.CorrelationFromContext(ctx); ok && correlation.TurnID != "" {
		attrs = append(attrs,
			logging.String("turn", string(correlation.TurnID)),
			logging.Int("round", int64(correlation.RoundIndex)),
		)
	}
	if result.tokens > cfg.targetTokens && result.degraded == "" {
		attrs = append(attrs, logging.String("degraded", "target_missed"))
	}
	r.logger.Info(ctx, "context compaction", attrs...)
}

// countResult describes the estimator identity for the published snapshot.
func (r *compactor) countResult(input contextwindow.CompactionRequest, layout engine.Layout, cfg normalizedConfig) usage.CountResult {
	provider := input.Invocation.Provider
	if provider == "" {
		provider = unselectedLabel
	}
	modelName := input.Invocation.Model
	if modelName == "" {
		modelName = unselectedLabel
	}
	return usage.CountResult{Accuracy: usage.AccuracyEstimate, Source: engine.EstimatorSource, Provider: provider, Model: modelName}
}
