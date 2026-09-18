package modelruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"unicode/utf8"

	"github.com/ingot-agent/ingot-abi/state"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/operation"
)

const (
	setupOperationName  = "config"
	setupOperationGroup = "model-runtime"
)

// ErrConfigConflict indicates that persisted configuration changed while an
// interactive update was in progress.
var ErrConfigConflict = errors.New("model.runtime configuration changed during interaction")

// setupOperation asks the Host for this Plugin's configuration through a
// structured interaction request and persists the answer in its own state
// scope. The Plugin owns validation and persistence; the Host never decodes
// plugin configuration.
type setupOperation struct {
	scope   state.Scope
	runtime *runtime
}

var _ operation.Operation = (*setupOperation)(nil)

func (*setupOperation) Definition() operation.Definition {
	return operation.Definition{
		Name:         setupOperationName,
		Description:  "Default provider and model used when a request leaves them empty.",
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
		return operation.Result{}, fmt.Errorf("model.runtime.config: state scope is required: %w", ErrInvalidConfig)
	}
	current, err := loadConfig(o.scope.Dir())
	if err != nil {
		return operation.Result{}, err
	}
	selection, err := o.runtime.snapshot(ctx)
	if err != nil {
		return operation.Result{}, err
	}
	providerNames := selection.names
	if len(providerNames) == 0 {
		return operation.Result{}, fmt.Errorf("model.runtime.config: no providers are available: %w", operation.ErrUnavailable)
	}
	providerField := interaction.Field{
		Name: "default_provider", Label: "Default Provider", Kind: interaction.FieldChoice, Required: true,
		Options: providerOptions(providerNames),
	}
	if len(providerNames) == 1 {
		providerField.Options = append([]interaction.Option{{Value: "", Label: "Automatic", Description: "Use the only available provider."}}, providerField.Options...)
	}
	if slices.Contains(providerNames, current.DefaultProvider) || (current.DefaultProvider == "" && len(providerNames) == 1) {
		providerField.Default = valuePointer(interaction.StringValue(current.DefaultProvider))
	}
	response, err := request.Interaction.Request(ctx, interaction.Request{
		Name:        setupOperationName,
		Description: "Default provider and model used when a request leaves them empty.",
		Fields: []interaction.Field{
			providerField,
			{Name: "default_model", Label: "Default Model", Kind: interaction.FieldString, Required: false, Default: &interaction.Value{Kind: interaction.ValueString, String: current.DefaultModel}},
		},
	})
	if err != nil {
		return operation.Result{}, err
	}
	// Start from the persisted configuration and overlay only the answers the
	// Host supplied, so an untouched field keeps its current value.
	updated := current
	if v, ok := answerString(response, "default_provider"); ok {
		updated.DefaultProvider = v
	} else {
		return operation.Result{}, fmt.Errorf("default_provider is required: %w", ErrInvalidConfig)
	}
	if v, ok := answerString(response, "default_model"); ok {
		updated.DefaultModel = v
	}
	if err := validateConfig(updated); err != nil {
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
	selection, err = o.runtime.snapshot(ctx)
	if err != nil {
		return operation.Result{}, err
	}
	if len(selection.names) == 0 {
		return operation.Result{}, fmt.Errorf("model.runtime.config: no providers are available: %w", operation.ErrUnavailable)
	}
	if updated.DefaultProvider == "" && len(selection.names) > 1 {
		return operation.Result{}, fmt.Errorf("default_provider must select one of the available providers: %w", ErrInvalidConfig)
	}
	if _, err := effectiveConfig(updated, selection.names); err != nil {
		return operation.Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return operation.Result{}, err
	}
	if err := saveConfig(o.scope.Dir(), updated); err != nil {
		return operation.Result{}, err
	}
	o.runtime.config.Store(&updated)
	return operation.Result{Output: json.RawMessage(`{"restart_required":false}`)}, nil
}

func validateConfig(config Config) error {
	if !utf8.ValidString(config.DefaultProvider) || !utf8.ValidString(config.DefaultModel) {
		return fmt.Errorf("provider or model contains invalid UTF-8: %w", ErrInvalidConfig)
	}
	return nil
}

func providerOptions(names []string) []interaction.Option {
	options := make([]interaction.Option, 0, len(names))
	for _, name := range names {
		options = append(options, interaction.Option{Value: name, Label: name})
	}
	return options
}

func effectiveConfig(config Config, providerNames []string) (Config, error) {
	if len(providerNames) == 0 {
		return config, nil
	}
	if config.DefaultProvider == "" {
		if len(providerNames) == 1 {
			config.DefaultProvider = providerNames[0]
		}
		return config, nil
	}
	for _, name := range providerNames {
		if config.DefaultProvider == name {
			return config, nil
		}
	}
	return Config{}, fmt.Errorf("default provider %q is unavailable: %w", config.DefaultProvider, ErrInvalidConfig)
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
