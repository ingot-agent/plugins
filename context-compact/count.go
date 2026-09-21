package contextcompact

import (
	"context"
	"fmt"
	"unicode/utf8"

	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/tool"
	"github.com/ingot-agent/sdk/usage"
)

func (r *compactor) countRequest(ctx context.Context, request model.Request, expected *usage.CountResult) (usage.CountResult, error) {
	if err := ctx.Err(); err != nil {
		return usage.CountResult{}, err
	}
	count, err := r.counter.CountInput(ctx, usage.CountRequest{Invocation: cloneRequest(request)})
	if err != nil {
		return usage.CountResult{}, fmt.Errorf("count context input: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return usage.CountResult{}, err
	}
	if count.InputTokens < 0 || count.Source == "" || count.Provider == "" || count.Model == "" ||
		!utf8.ValidString(count.Source) || !utf8.ValidString(count.Provider) || !utf8.ValidString(count.Model) || accuracyBit(count.Accuracy) == 0 {
		return usage.CountResult{}, ErrInvalidCount
	}
	if (request.Provider != "" && request.Provider != count.Provider) || (request.Model != "" && request.Model != count.Model) {
		return usage.CountResult{}, fmt.Errorf("counter changed explicit model selection: %w", ErrInvalidCount)
	}
	if r.cfg.allowedAccuracies&accuracyBit(count.Accuracy) == 0 {
		return usage.CountResult{}, fmt.Errorf("accuracy %q: %w", count.Accuracy, ErrUnsupportedAccuracy)
	}
	if expected != nil && (count.Source != expected.Source || count.Accuracy != expected.Accuracy || count.Provider != expected.Provider || count.Model != expected.Model) {
		return usage.CountResult{}, fmt.Errorf("counting identity changed during compaction: %w", ErrInvalidCount)
	}
	return count, nil
}

// A standalone count is a selection/budget scale, not an additive contribution
// to the full invocation. Tools are explicitly empty to prevent default injection.
func (r *compactor) countMessages(ctx context.Context, base model.Request, messages []model.Message, identity usage.CountResult) (int64, error) {
	request := cloneRequest(base)
	request.Provider, request.Model = identity.Provider, identity.Model
	request.Messages = cloneMessages(messages)
	request.Tools = []tool.Definition{}
	count, err := r.countRequest(ctx, request, &identity)
	return count.InputTokens, err
}

func (r *compactor) memoryTokens(ctx context.Context, request model.Request, chain chainState, identity usage.CountResult) (int64, int64, error) {
	if len(chain.active) == 0 {
		return 0, 0, nil
	}
	messages, err := memoryMessages(chain)
	if err != nil {
		return 0, 0, err
	}
	memory, err := r.countMessages(ctx, request, messages, identity)
	if err != nil {
		return 0, 0, err
	}
	if len(chain.state) == 0 {
		return memory, 0, nil
	}
	snapshot, err := snapshotMessage(chain.revision, stateSnapshot(chain.state))
	if err != nil {
		return 0, 0, err
	}
	stateTokens, err := r.countMessages(ctx, request, []model.Message{{Role: model.RoleAssistant, Content: content.FromText(snapshot)}}, identity)
	return memory, stateTokens, err
}
