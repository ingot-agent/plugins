package contextcompact

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/contextwindow"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/usage"
)

func (r *compactor) Compact(ctx context.Context, input contextwindow.CompactionRequest) (contextwindow.CompactionResult, error) {
	snapshot := &compactor{model: r.model, counter: r.counter, store: r.store, cfg: *r.config.Load(), gates: r.gates}
	return snapshot.compact(ctx, input)
}

func (r *compactor) compact(ctx context.Context, input contextwindow.CompactionRequest) (contextwindow.CompactionResult, error) {
	if ctx == nil {
		return contextwindow.CompactionResult{}, fmt.Errorf("nil context: %w", ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return contextwindow.CompactionResult{}, err
	}
	if input.SessionID == "" {
		return contextwindow.CompactionResult{}, fmt.Errorf("session_id is required: %w", ErrInvalidRequest)
	}
	request := cloneRequest(input.Invocation)
	layout, err := inspectRequest(request, r.cfg.recentRounds)
	if err != nil {
		return contextwindow.CompactionResult{}, err
	}
	if _, err := canonicalRequestBytes(request); err != nil {
		return contextwindow.CompactionResult{}, fmt.Errorf("canonicalize invocation: %w: %w", ErrInvalidRequest, err)
	}
	release, err := r.gates.acquire(ctx, string(input.SessionID))
	if err != nil {
		return contextwindow.CompactionResult{}, err
	}
	defer release()

	identity, err := r.countRequest(ctx, request, nil)
	if err != nil {
		return contextwindow.CompactionResult{}, err
	}
	// Reuse the counter's resolved defaults when the summarizer inherits this
	// provider. A different summary provider may have its own default model.
	summaryBase := cloneRequest(request)
	if summaryBase.Provider == "" {
		summaryBase.Provider = identity.Provider
	}
	if summaryBase.Model == "" && (r.cfg.provider == "" || r.cfg.provider == identity.Provider) {
		summaryBase.Model = identity.Model
	}
	policy, err := r.policyDigest(summaryBase, identity)
	if err != nil {
		return contextwindow.CompactionResult{}, err
	}
	provider, modelName := r.summarySelection(summaryBase)
	chain, err := r.loadChain(ctx, input.SessionID, policy, provider, modelName, layout)
	if err != nil {
		return contextwindow.CompactionResult{}, err
	}
	messages, current, err := r.materializedTokens(ctx, request, layout, chain, identity)
	if err != nil {
		return contextwindow.CompactionResult{}, err
	}
	compactHistory := current >= r.cfg.triggerInputTokens
	budget := &callBudget{limit: r.cfg.maxSummaryPasses}
	expandedEnd := 0

	for {
		if err := ctx.Err(); err != nil {
			return contextwindow.CompactionResult{}, err
		}
		memory, stateTokens, err := r.memoryTokens(ctx, request, chain, identity)
		if err != nil {
			return contextwindow.CompactionResult{}, err
		}
		rollup := memory >= r.cfg.memoryTriggerTokens || stateTokens >= r.cfg.stateTriggerTokens
		if !rollup && (!compactHistory || current <= r.cfg.targetInputTokens) {
			return contextwindow.CompactionResult{Messages: cloneMessages(messages), Changed: chain.lastSequence != 0}, nil
		}
		var checkpoint persistedCheckpoint
		var next chainState
		if rollup {
			checkpoint, next, err = r.prepareRollup(ctx, summaryBase, policy, layout, chain, budget)
		} else {
			var end int
			_, end, err = r.selectSource(ctx, request, identity, layout, chain.covered)
			if err == nil {
				checkpoint, next, err = r.prepareSegment(ctx, summaryBase, policy, layout, chain, max(end, expandedEnd), budget)
			}
		}
		if err != nil {
			return contextwindow.CompactionResult{}, err
		}
		next.maxSequence = checkpoint.Sequence
		candidate, candidateTokens, err := r.materializedTokens(ctx, request, layout, next, identity)
		if err != nil {
			return contextwindow.CompactionResult{}, err
		}
		if candidateTokens >= current {
			// A short old round may cost more to summarize than to retain. Try
			// including the next complete round before declaring it uncompactable.
			if !rollup {
				for _, round := range layout.rounds {
					if round.start == checkpoint.CoveredMessages && !containsMedia(layout.conversation[round.start:round.end]) {
						expandedEnd = round.end
						break
					}
				}
				if expandedEnd > checkpoint.CoveredMessages {
					continue
				}
			}
			return contextwindow.CompactionResult{}, fmt.Errorf("summary did not reduce input tokens: %w", ErrContextUncompactable)
		}
		memory, stateTokens, err = r.memoryTokens(ctx, request, next, identity)
		if err != nil {
			return contextwindow.CompactionResult{}, err
		}
		if rollup {
			if memory > r.cfg.memoryTargetTokens || stateTokens > r.cfg.stateTargetTokens {
				return contextwindow.CompactionResult{}, fmt.Errorf("rollup did not reach memory and state token targets: %w", ErrContextUncompactable)
			}
		}
		if err := ctx.Err(); err != nil {
			return contextwindow.CompactionResult{}, err
		}
		if err := r.appendCheckpoint(ctx, input.SessionID, checkpoint); err != nil {
			return contextwindow.CompactionResult{}, err
		}
		chain, messages, current = next, candidate, candidateTokens
		expandedEnd = 0
	}
}

func (r *compactor) prepareSegment(ctx context.Context, request model.Request, policy string, layout messageLayout, chain chainState, end int, budget *callBudget) (persistedCheckpoint, chainState, error) {
	output, provider, modelName, err := r.summarizeSegment(ctx, request, chain.state, layout.conversation[chain.covered:end], budget)
	if err != nil {
		return persistedCheckpoint{}, chainState{}, err
	}
	digest, err := messageDigest(layout.conversation[:end])
	if err != nil {
		return persistedCheckpoint{}, chainState{}, err
	}
	checkpoint := persistedCheckpoint{
		Sequence: chain.maxSequence + 1, ParentSequence: chain.lastSequence, Mode: checkpointModeSegment,
		PolicyDigest: policy, CoveredMessages: end, SourceDigest: digest, Summary: output.Summary,
		BaseRevision: chain.revision, Revision: chain.revision + 1, Operations: cloneOperations(output.Operations),
		Provider: provider, Model: modelName,
	}
	next, err := extendChain(chain, checkpoint)
	return checkpoint, next, err
}

func (r *compactor) prepareRollup(ctx context.Context, request model.Request, policy string, layout messageLayout, chain chainState, budget *callBudget) (persistedCheckpoint, chainState, error) {
	summaries := make([]string, len(chain.active))
	for i, checkpoint := range chain.active {
		summaries[i] = checkpoint.Summary
	}
	output, provider, modelName, err := r.summarizeRollup(ctx, request, chain.state, summaries, budget)
	if err != nil {
		return persistedCheckpoint{}, chainState{}, err
	}
	digest, err := messageDigest(layout.conversation[:chain.covered])
	if err != nil {
		return persistedCheckpoint{}, chainState{}, err
	}
	retained := cloneState(chain.state)
	if err := applyOperations(retained, output.Operations); err != nil {
		return persistedCheckpoint{}, chainState{}, err
	}
	checkpoint := persistedCheckpoint{
		Sequence: chain.maxSequence + 1, ParentSequence: chain.lastSequence, Mode: checkpointModeRollup,
		PolicyDigest: policy, CoveredMessages: chain.covered, SourceDigest: digest, Summary: output.Summary,
		BaseRevision: chain.revision, Revision: chain.revision + 1, Operations: cloneOperations(output.Operations),
		StateSnapshot: stateSnapshot(retained), Provider: provider, Model: modelName,
	}
	next, err := extendChain(chain, checkpoint)
	return checkpoint, next, err
}

func (r *compactor) materializedTokens(ctx context.Context, base model.Request, layout messageLayout, chain chainState, identity usage.CountResult) ([]model.Message, int64, error) {
	messages, err := materializeMessages(layout, chain)
	if err != nil {
		return nil, 0, err
	}
	request := cloneRequest(base)
	request.Messages = messages
	count, err := r.countRequest(ctx, request, &identity)
	return messages, count.InputTokens, err
}

// Recent rounds are a selection preference, never a checkpoint validity boundary.
func (r *compactor) selectSource(ctx context.Context, request model.Request, identity usage.CountResult, layout messageLayout, covered int) (int, int, error) {
	if covered >= layout.completeEnd || !isRoundBoundary(layout, covered) {
		return 0, 0, fmt.Errorf("no complete round remains: %w", ErrContextUncompactable)
	}
	limit := layout.eligibleEnd
	if covered >= limit {
		limit = layout.completeEnd
	}
	end := covered
	for _, round := range layout.rounds {
		if round.start < covered {
			continue
		}
		if round.end > limit || containsMedia(layout.conversation[round.start:round.end]) {
			break
		}
		end = round.end
		count, err := r.countMessages(ctx, request, layout.conversation[covered:end], identity)
		if err != nil {
			return 0, 0, err
		}
		if count >= r.cfg.summaryChunkTokens {
			break
		}
	}
	if end == covered {
		return 0, 0, fmt.Errorf("no eligible text round remains: %w", ErrContextUncompactable)
	}
	return covered, end, nil
}

func containsMedia(messages []model.Message) bool {
	for _, message := range messages {
		for _, part := range message.Content {
			if part.Kind != content.KindText {
				return true
			}
		}
	}
	return false
}

func jsonMessages(messages []model.Message) ([]byte, error) {
	return json.Marshal(projectMessages(messages))
}
