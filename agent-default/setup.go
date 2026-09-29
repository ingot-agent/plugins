package agentdefault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"

	"github.com/ingot-agent/ingot-abi/state"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/operation"
)

const (
	setupOperationName  = "config"
	setupOperationGroup = "agent-default"
	overrideInherit     = "inherit"
	overrideSet         = "override"
)

var ErrConfigConflict = errors.New("agent.default configuration changed during interaction")

// setupOperation asks the Host for this Plugin's configuration through a
// structured interaction request and persists the answer in its own state
// scope. temperature and max_tokens are optional overrides whose inherit or
// override intent is requested explicitly before a typed value is collected.
type setupOperation struct {
	scope   state.Scope
	runtime *runtime
}

var _ operation.Operation = (*setupOperation)(nil)

func (*setupOperation) Definition() operation.Definition {
	return operation.Definition{
		Name:         setupOperationName,
		Description:  "Review and update the agent loop defaults.",
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
		return operation.Result{}, fmt.Errorf("agent.default config: state scope is required: %w", ErrInvalidConfig)
	}
	current, err := loadConfig(o.scope.Dir())
	if err != nil {
		return operation.Result{}, err
	}
	maxRoundsDefault := int64(current.MaxRounds)
	if maxRoundsDefault == 0 {
		maxRoundsDefault = defaultMaxRounds
	}
	temperatureMode := overrideInherit
	if current.Temperature != nil {
		temperatureMode = overrideSet
	}
	maxTokensMode := overrideInherit
	if current.MaxTokens != nil {
		maxTokensMode = overrideSet
	}
	response, err := request.Interaction.Request(ctx, interaction.Request{
		Name:        setupOperationName,
		Description: "Generation limits and maximum rounds for each turn.",
		Fields: []interaction.Field{
			{Name: "temperature_mode", Label: "Temperature", Kind: interaction.FieldChoice, Required: true, Default: valuePointer(interaction.StringValue(temperatureMode)), Options: overrideOptions()},
			{Name: "max_tokens_mode", Label: "Max tokens", Kind: interaction.FieldChoice, Required: true, Default: valuePointer(interaction.StringValue(maxTokensMode)), Options: overrideOptions()},
			{Name: "max_rounds", Label: "Max rounds", Kind: interaction.FieldInteger, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: maxRoundsDefault}},
		},
	})
	if err != nil {
		return operation.Result{}, err
	}
	updated := current
	selectedTemperatureMode, ok := answerString(response, "temperature_mode")
	temperatureMode = selectedTemperatureMode
	if !ok || (temperatureMode != overrideInherit && temperatureMode != overrideSet) {
		return operation.Result{}, fmt.Errorf("temperature_mode is invalid: %w", ErrInvalidConfig)
	}
	selectedMaxTokensMode, ok := answerString(response, "max_tokens_mode")
	maxTokensMode = selectedMaxTokensMode
	if !ok || (maxTokensMode != overrideInherit && maxTokensMode != overrideSet) {
		return operation.Result{}, fmt.Errorf("max_tokens_mode is invalid: %w", ErrInvalidConfig)
	}
	if temperatureMode == overrideInherit {
		updated.Temperature = nil
	}
	if maxTokensMode == overrideInherit {
		updated.MaxTokens = nil
	}
	if temperatureMode == overrideSet || maxTokensMode == overrideSet {
		fields := make([]interaction.Field, 0, 2)
		if temperatureMode == overrideSet {
			fields = append(fields, interaction.Field{Name: "temperature", Label: "Temperature", Description: "A number between 0 and 2.", Kind: interaction.FieldNumber, Required: true, Default: optionalFloat(current.Temperature)})
		}
		if maxTokensMode == overrideSet {
			fields = append(fields, interaction.Field{Name: "max_tokens", Label: "Max tokens", Kind: interaction.FieldInteger, Required: true, Default: optionalInt(current.MaxTokens)})
		}
		overrides, err := request.Interaction.Request(ctx, interaction.Request{Name: setupOperationName + ".overrides", Description: "Generation overrides.", Fields: fields})
		if err != nil {
			return operation.Result{}, err
		}
		if temperatureMode == overrideSet {
			value, ok := answerNumber(overrides, "temperature")
			if !ok {
				return operation.Result{}, fmt.Errorf("temperature is required: %w", ErrInvalidConfig)
			}
			updated.Temperature = &value
		}
		if maxTokensMode == overrideSet {
			value, ok := answerInteger(overrides, "max_tokens")
			if !ok {
				return operation.Result{}, fmt.Errorf("max_tokens is required: %w", ErrInvalidConfig)
			}
			converted := int(value)
			updated.MaxTokens = &converted
		}
	}
	if v, ok := answerInteger(response, "max_rounds"); ok {
		updated.MaxRounds = int(v)
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
	o.runtime.config.Store(&effective)
	return operation.Result{Output: json.RawMessage(`{"restart_required":false}`)}, nil
}

// validateConfig applies the same generation limits at construction and in the
// setup Operation.
func validateConfig(cfg Config) error {
	_, err := normalizeConfig(cfg)
	return err
}

func normalizeConfig(cfg Config) (Config, error) {
	if cfg.Temperature != nil && (math.IsNaN(*cfg.Temperature) || math.IsInf(*cfg.Temperature, 0) || *cfg.Temperature < 0 || *cfg.Temperature > 2) {
		return Config{}, fmt.Errorf("temperature must be in [0,2]: %w", ErrInvalidConfig)
	}
	if cfg.MaxTokens != nil && *cfg.MaxTokens < 1 {
		return Config{}, fmt.Errorf("max_tokens must be positive: %w", ErrInvalidConfig)
	}
	maxRounds := cfg.MaxRounds
	if maxRounds == 0 {
		maxRounds = defaultMaxRounds
	}
	if maxRounds < 1 {
		return Config{}, fmt.Errorf("max_rounds must be positive: %w", ErrInvalidConfig)
	}
	cfg.MaxRounds = maxRounds
	return cfg, nil
}

func overrideOptions() []interaction.Option {
	return []interaction.Option{{Value: overrideInherit, Label: "Inherit"}, {Value: overrideSet, Label: "Override"}}
}

func valuePointer(value interaction.Value) *interaction.Value { return &value }

func optionalFloat(value *float64) *interaction.Value {
	if value == nil {
		return nil
	}
	result := interaction.NumberValue(*value)
	return &result
}

func optionalInt(value *int) *interaction.Value {
	if value == nil {
		return nil
	}
	result := interaction.IntegerValue(int64(*value))
	return &result
}

func answerString(response interaction.Response, name string) (string, bool) {
	for _, answer := range response.Values {
		if answer.Name == name && answer.Value.Kind == interaction.ValueString {
			return answer.Value.String, true
		}
	}
	return "", false
}

func answerInteger(response interaction.Response, name string) (int64, bool) {
	for _, answer := range response.Values {
		if answer.Name == name && answer.Value.Kind == interaction.ValueInteger {
			return answer.Value.Integer, true
		}
	}
	return 0, false
}

func answerNumber(response interaction.Response, name string) (float64, bool) {
	for _, answer := range response.Values {
		if answer.Name == name && answer.Value.Kind == interaction.ValueNumber {
			return answer.Value.Number, true
		}
	}
	return 0, false
}
