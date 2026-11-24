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
)

// ErrConfigConflict reports that the persisted configuration changed while the
// interaction was open, so the submitted answer no longer applies.
var ErrConfigConflict = errors.New("context.compact configuration changed during interaction")

const (
	setupOperationName  = "config"
	setupOperationGroup = "context-compact"
)

// setupOperation asks the Host for this Plugin's configuration and persists the
// answer in its own state scope.
type setupOperation struct {
	scope           state.Scope
	providerSources []model.ProviderSource
	compactor       *compactor
	loader          *configLoader
}

var _ operation.Operation = (*setupOperation)(nil)

func newSetupOperation(scope state.Scope, sources []model.ProviderSource, compactor *compactor, loader *configLoader) *setupOperation {
	return &setupOperation{
		scope: scope, providerSources: append([]model.ProviderSource(nil), sources...),
		compactor: compactor, loader: loader,
	}
}

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
	current, _, err := loadConfig(o.scope.Dir())
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
	providerField := interaction.Field{
		Name: "provider", Label: "Provider", Required: true, Kind: interaction.FieldChoice,
		Description: "Empty uses the provider of the request being compacted.",
		Options:     []interaction.Option{{Value: "", Label: "Request provider"}},
	}
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
			{
				Name: "model", Label: "Model", Kind: interaction.FieldString,
				Description: "Empty uses the model of the request being compacted.",
				Default:     valuePointer(interaction.StringValue(current.Model)),
			},
			{
				Name: "trigger_tokens", Label: "Trigger Tokens", Kind: interaction.FieldInteger,
				Description: "Compact only when the request exceeds this input size.",
				Default:     valuePointer(interaction.IntegerValue(effective.triggerTokens)),
			},
			{
				Name: "target_tokens", Label: "Target Tokens", Kind: interaction.FieldInteger,
				Description: "Size to shrink to once the trigger is crossed. Must be below the trigger.",
				Default:     valuePointer(interaction.IntegerValue(effective.targetTokens)),
			},
			{
				Name: "protect_recent_tokens", Label: "Protect Recent Tokens", Kind: interaction.FieldInteger,
				Description: "Recent tokens kept at full fidelity. A budget, not a round count.",
				Default:     valuePointer(interaction.IntegerValue(effective.protectRecentTokens)),
			},
			{
				Name: "summary_max_tokens", Label: "Summary Max Tokens", Kind: interaction.FieldInteger,
				Description: "Output budget for one summary call.",
				Default:     valuePointer(interaction.IntegerValue(int64(effective.summaryMaxTokens))),
			},
		},
	})
	if err != nil {
		return operation.Result{}, err
	}

	updated := current
	if value, ok := answerString(response, "provider"); ok {
		updated.Provider = value
	}
	if value, ok := answerString(response, "model"); ok {
		updated.Model = value
	}
	if value, ok := answerInteger(response, "trigger_tokens"); ok {
		updated.TriggerTokens = value
	}
	if value, ok := answerInteger(response, "target_tokens"); ok {
		updated.TargetTokens = value
	}
	if value, ok := answerInteger(response, "protect_recent_tokens"); ok {
		updated.ProtectRecentTokens = value
	}
	if value, ok := answerInteger(response, "summary_max_tokens"); ok {
		updated.SummaryMaxTokens = int(value)
	}

	if _, err := normalizeConfig(updated); err != nil {
		return operation.Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return operation.Result{}, err
	}

	configCommitMu.Lock()
	defer configCommitMu.Unlock()
	latest, _, err := loadConfig(o.scope.Dir())
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
	// The loader picks the change up immediately: v2 reloads on file change, so
	// an Operation never has to reach into running state.
	if _, err := o.loader.update(updated); err != nil {
		return operation.Result{}, err
	}
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
