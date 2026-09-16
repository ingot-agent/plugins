package toolask

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/ingot-agent/ingot-abi/state"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/operation"
)

var ErrConfigConflict = errors.New("tool.ask configuration changed during interaction")

const (
	setupOperationName  = "config"
	setupOperationGroup = "tool-ask"
)

// setupOperation asks the Host for this Plugin's configuration through a
// structured interaction request and persists the answer in its own state
// scope. The Plugin owns validation and persistence; the Host never decodes
// plugin configuration.
type setupOperation struct {
	scope  state.Scope
	active Config
}

var _ operation.Operation = (*setupOperation)(nil)

func (*setupOperation) Definition() operation.Definition {
	return operation.Definition{
		Name:         setupOperationName,
		Description:  "Limits for the ask_user tool.",
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
		return operation.Result{}, fmt.Errorf("tool.ask.config: state scope is required: %w", ErrInvalidConfig)
	}
	current, err := loadConfig(o.scope.Dir())
	if err != nil {
		return operation.Result{}, err
	}
	effectiveCurrent, err := normalizeConfig(current)
	if err != nil {
		return operation.Result{}, err
	}
	response, err := request.Interaction.Request(ctx, interaction.Request{
		Name:        setupOperationName,
		Description: "Limits for the ask_user tool.",
		Fields: []interaction.Field{
			{Name: "max_prompt_bytes", Label: "Max Prompt Bytes", Kind: interaction.FieldInteger, Required: false, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: int64(effectiveCurrent.MaxPromptBytes)}},
			{Name: "max_response_bytes", Label: "Max Response Bytes", Kind: interaction.FieldInteger, Required: false, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: int64(effectiveCurrent.MaxResponseBytes)}},
			{Name: "max_options", Label: "Max Options", Kind: interaction.FieldInteger, Required: false, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: int64(effectiveCurrent.MaxOptions)}},
			{Name: "max_options_bytes", Label: "Max Options Bytes", Kind: interaction.FieldInteger, Required: false, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: int64(effectiveCurrent.MaxOptionsBytes)}},
		},
	})
	if err != nil {
		return operation.Result{}, err
	}
	// Start from the persisted configuration and overlay only the answers the
	// Host supplied, so an untouched field keeps its current value.
	updated := current
	if v, ok := answerInteger(response, "max_prompt_bytes"); ok {
		updated.MaxPromptBytes = int(v)
	}
	if v, ok := answerInteger(response, "max_response_bytes"); ok {
		updated.MaxResponseBytes = int(v)
	}
	if v, ok := answerInteger(response, "max_options"); ok {
		updated.MaxOptions = int(v)
	}
	if v, ok := answerInteger(response, "max_options_bytes"); ok {
		updated.MaxOptionsBytes = int(v)
	}
	effective, err := normalizeConfig(updated)
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
	output, err := json.Marshal(map[string]any{"restart_required": effective != o.active})
	if err != nil {
		return operation.Result{}, err
	}
	return operation.Result{Output: output}, nil
}

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
