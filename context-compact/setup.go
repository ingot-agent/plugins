package contextcompact

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"

	"github.com/ingot-agent/ingot-abi/state"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/operation"
	"github.com/ingot-agent/sdk/usage"
)

var ErrConfigConflict = errors.New("context.compact configuration changed during interaction")

const (
	setupOperationName  = "config"
	setupOperationGroup = "context-compact"
)

// setupOperation asks the Host for this Plugin's configuration through a
// structured interaction request and persists the answer in its own state
// scope. The Plugin owns validation and persistence; the Host never decodes
// plugin configuration.
type setupOperation struct {
	scope           state.Scope
	providerSources []model.ProviderSource
	compactor       *compactor
}

var _ operation.Operation = (*setupOperation)(nil)

func (*setupOperation) Definition() operation.Definition {
	return operation.Definition{
		Name:         setupOperationName,
		Description:  "Context compaction thresholds and summarization limits.",
		Group:        setupOperationGroup,
		InputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`),
		OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["restart_required"],"properties":{"restart_required":{"type":"boolean"}}}`),
	}
}

func (o *setupOperation) Invoke(ctx context.Context, request operation.Request) (operation.Result, error) {
	if err := ctx.Err(); err != nil {
		return operation.Result{}, err
	}
	if o.scope == nil || o.scope.Dir() == "" {
		return operation.Result{}, fmt.Errorf("context.compact.config: state scope is required: %w", ErrInvalidConfig)
	}
	current, err := loadConfig(o.scope.Dir())
	if err != nil {
		return operation.Result{}, err
	}
	effective, err := normalizeConfig(current)
	if err != nil {
		return operation.Result{}, err
	}
	providerNames, err := currentProviderNames(ctx, o.providerSources)
	if err != nil {
		return operation.Result{}, err
	}
	providerField := interaction.Field{Name: "provider", Label: "Provider", Description: "Empty uses the provider from the request being compacted.", Kind: interaction.FieldChoice, Required: true, Options: []interaction.Option{{Value: "", Label: "Request provider"}}}
	if current.Provider == "" || slices.Contains(providerNames, current.Provider) {
		providerField.Default = valuePointer(interaction.StringValue(current.Provider))
	}
	for _, name := range providerNames {
		providerField.Options = append(providerField.Options, interaction.Option{Value: name, Label: name})
	}
	response, err := request.Interaction.Request(ctx, interaction.Request{
		Name:        setupOperationName,
		Description: "Context compaction thresholds and summarization limits.",
		Fields: []interaction.Field{
			providerField,
			{Name: "model", Label: "Model", Kind: interaction.FieldString, Required: false, Default: &interaction.Value{Kind: interaction.ValueString, String: current.Model}},
			{Name: "trigger_input_tokens", Label: "Trigger Input Tokens", Kind: interaction.FieldInteger, Required: false, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: int64(effective.triggerInputTokens)}},
			{Name: "target_input_tokens", Label: "Target Input Tokens", Kind: interaction.FieldInteger, Required: false, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: int64(effective.targetInputTokens)}},
			{Name: "recent_rounds", Label: "Recent Rounds", Kind: interaction.FieldInteger, Required: false, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: int64(effective.recentRounds)}},
			{Name: "summary_chunk_tokens", Label: "Summary Chunk Tokens", Kind: interaction.FieldInteger, Required: false, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: int64(effective.summaryChunkTokens)}},
			{Name: "summary_max_tokens", Label: "Summary Max Tokens", Kind: interaction.FieldInteger, Required: false, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: int64(effective.summaryMaxTokens)}},
			{Name: "summary_max_bytes", Label: "Summary Max Bytes", Kind: interaction.FieldInteger, Required: false, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: int64(effective.summaryMaxBytes)}},
			{Name: "summary_input_tokens", Label: "Summary Input Tokens", Kind: interaction.FieldInteger, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: int64(effective.summaryInputTokens)}},
			{Name: "rollup_max_tokens", Label: "Rollup Max Tokens", Kind: interaction.FieldInteger, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: int64(effective.rollupMaxTokens)}},
			{Name: "memory_trigger_tokens", Label: "Memory Trigger Tokens", Kind: interaction.FieldInteger, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: int64(effective.memoryTriggerTokens)}},
			{Name: "memory_target_tokens", Label: "Memory Target Tokens", Kind: interaction.FieldInteger, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: int64(effective.memoryTargetTokens)}},
			{Name: "state_trigger_tokens", Label: "State Trigger Tokens", Kind: interaction.FieldInteger, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: int64(effective.stateTriggerTokens)}},
			{Name: "state_target_tokens", Label: "State Target Tokens", Kind: interaction.FieldInteger, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: int64(effective.stateTargetTokens)}},
			{Name: "allowed_accuracies", Label: "Allowed Count Accuracies", Kind: interaction.FieldMultiChoice, Required: true, Default: valuePointer(interaction.Value{Kind: interaction.ValueStrings, Strings: configuredAccuracyNames(current)}), Options: []interaction.Option{{Value: "exact", Label: "Exact"}, {Value: "upper_bound", Label: "Upper Bound"}, {Value: "estimate", Label: "Estimate"}}},
			{Name: "max_summary_passes", Label: "Max Summary Passes", Kind: interaction.FieldInteger, Required: false, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: int64(effective.maxSummaryPasses)}},
		},
	})
	if err != nil {
		return operation.Result{}, err
	}
	// Start from the persisted configuration and overlay only the answers the
	// Host supplied, so an untouched field keeps its current value.
	updated := current
	if v, ok := answerString(response, "provider"); ok {
		updated.Provider = v
	}
	if v, ok := answerString(response, "model"); ok {
		updated.Model = v
	}
	if v, ok := answerInteger(response, "trigger_input_tokens"); ok {
		updated.TriggerInputTokens = v
	}
	if v, ok := answerInteger(response, "target_input_tokens"); ok {
		updated.TargetInputTokens = v
	}
	if v, ok := answerInteger(response, "recent_rounds"); ok {
		updated.RecentRounds = int(v)
	}
	if v, ok := answerInteger(response, "summary_chunk_tokens"); ok {
		updated.SummaryChunkTokens = v
	}
	if v, ok := answerInteger(response, "summary_max_tokens"); ok {
		updated.SummaryMaxTokens = int(v)
	}
	if v, ok := answerInteger(response, "summary_max_bytes"); ok {
		updated.SummaryMaxBytes = int(v)
	}
	if v, ok := answerInteger(response, "max_summary_passes"); ok {
		updated.MaxSummaryPasses = int(v)
	}
	if v, ok := answerInteger(response, "summary_input_tokens"); ok {
		updated.SummaryInputTokens = v
	}
	if v, ok := answerInteger(response, "rollup_max_tokens"); ok {
		updated.RollupMaxTokens = int(v)
	}
	if v, ok := answerInteger(response, "memory_trigger_tokens"); ok {
		updated.MemoryTriggerTokens = v
	}
	if v, ok := answerInteger(response, "memory_target_tokens"); ok {
		updated.MemoryTargetTokens = v
	}
	if v, ok := answerInteger(response, "state_trigger_tokens"); ok {
		updated.StateTriggerTokens = v
	}
	if v, ok := answerInteger(response, "state_target_tokens"); ok {
		updated.StateTargetTokens = v
	}
	for _, answer := range response.Values {
		if answer.Name == "allowed_accuracies" {
			if answer.Value.Kind != interaction.ValueStrings {
				return operation.Result{}, fmt.Errorf("allowed_accuracies requires choices: %w", ErrInvalidConfig)
			}
			updated.AllowedAccuracies = make([]usage.Accuracy, len(answer.Value.Strings))
			for i, value := range answer.Value.Strings {
				updated.AllowedAccuracies[i] = usage.Accuracy(value)
			}
		}
	}
	providerNames, err = currentProviderNames(ctx, o.providerSources)
	if err != nil {
		return operation.Result{}, err
	}
	normalized, err := normalizeConfigForProviders(updated, providerNames)
	if err != nil {
		return operation.Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return operation.Result{}, err
	}
	configCommitMu.Lock()
	defer configCommitMu.Unlock()
	latest, err := loadConfig(o.scope.Dir())
	if err != nil {
		return operation.Result{}, err
	}
	if !reflect.DeepEqual(latest, current) {
		return operation.Result{}, ErrConfigConflict
	}
	if err := ctx.Err(); err != nil {
		return operation.Result{}, err
	}
	if err := saveConfig(o.scope.Dir(), updated); err != nil {
		return operation.Result{}, err
	}
	o.compactor.config.Store(&normalized)
	return operation.Result{Output: json.RawMessage(`{"restart_required":false}`)}, nil
}

func valuePointer(value interaction.Value) *interaction.Value { return &value }

func answerInteger(response interaction.Response, name string) (int64, bool) {
	for _, answer := range response.Values {
		if answer.Name != name {
			continue
		}
		if answer.Value.Kind != interaction.ValueInteger {
			return 0, false
		}
		return answer.Value.Integer, true
	}
	return 0, false
}

func answerString(response interaction.Response, name string) (string, bool) {
	for _, answer := range response.Values {
		if answer.Name != name {
			continue
		}
		if answer.Value.Kind != interaction.ValueString {
			return "", false
		}
		return answer.Value.String, true
	}
	return "", false
}

func answerNumber(response interaction.Response, name string) (float64, bool) {
	for _, answer := range response.Values {
		if answer.Name != name {
			continue
		}
		if answer.Value.Kind != interaction.ValueNumber {
			return 0, false
		}
		return answer.Value.Number, true
	}
	return 0, false
}

func answerBoolean(response interaction.Response, name string) (bool, bool) {
	for _, answer := range response.Values {
		if answer.Name != name {
			continue
		}
		if answer.Value.Kind != interaction.ValueBoolean {
			return false, false
		}
		return answer.Value.Boolean, true
	}
	return false, false
}

func configuredAccuracyNames(cfg Config) []string {
	if cfg.AllowedAccuracies == nil {
		return []string{"exact", "upper_bound", "estimate"}
	}
	values := make([]string, len(cfg.AllowedAccuracies))
	for i, value := range cfg.AllowedAccuracies {
		values[i] = string(value)
	}
	return values
}
