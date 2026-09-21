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
	"github.com/ingot-agent/sdk/model"
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
	providerResponse, err := request.Interaction.Request(ctx, interaction.Request{
		Name:        setupOperationName,
		Description: "Select the default model provider.",
		Fields:      []interaction.Field{providerField},
	})
	if err != nil {
		return operation.Result{}, err
	}
	selectedProvider, ok := answerString(providerResponse, "default_provider")
	if !ok {
		return operation.Result{}, fmt.Errorf("default_provider is required: %w", ErrInvalidConfig)
	}
	configuredProvider := selectedProvider
	if selectedProvider == "" && len(providerNames) == 1 {
		selectedProvider = providerNames[0]
	}
	provider, ok := selection.providers[selectedProvider]
	if !ok {
		return operation.Result{}, fmt.Errorf("default provider %q is unavailable: %w", selectedProvider, ErrInvalidConfig)
	}

	modelField := interaction.Field{Name: "default_model", Label: "Default Model", Required: len(provider.Models) != 0}
	if len(provider.Models) == 0 {
		modelField.Kind = interaction.FieldString
	} else {
		modelField.Kind = interaction.FieldChoice
		for _, candidate := range provider.Models {
			modelField.Options = append(modelField.Options, interaction.Option{Value: candidate.Name, Label: candidate.Name})
		}
	}
	if _, exists := findProviderModel(provider.Models, current.DefaultModel); exists || (len(provider.Models) == 0 && current.DefaultModel != "") {
		modelField.Default = valuePointer(interaction.StringValue(current.DefaultModel))
	}
	modelResponse, err := request.Interaction.Request(ctx, interaction.Request{
		Name: setupOperationName, Description: "Select the default model.", Fields: []interaction.Field{modelField},
	})
	if err != nil {
		return operation.Result{}, err
	}
	selectedModel, ok := answerString(modelResponse, "default_model")
	if !ok {
		selectedModel = current.DefaultModel
	}
	if len(provider.Models) != 0 && selectedModel == "" {
		return operation.Result{}, fmt.Errorf("default_model is required: %w", ErrInvalidConfig)
	}
	selectedModelEntry, modelDeclared := findProviderModel(provider.Models, selectedModel)
	if len(provider.Models) != 0 && !modelDeclared {
		return operation.Result{}, fmt.Errorf("default model %q is unavailable from provider %q: %w", selectedModel, selectedProvider, ErrInvalidConfig)
	}

	effortField := interaction.Field{
		Name: "default_reasoning_effort", Label: "Default Reasoning Effort", Kind: interaction.FieldChoice, Required: true,
		Options: []interaction.Option{{Value: "", Label: "Provider default"}},
	}
	if modelDeclared {
		for _, effort := range selectedModelEntry.ReasoningEfforts {
			effortField.Options = append(effortField.Options, interaction.Option{Value: string(effort), Label: string(effort)})
		}
	}
	if current.DefaultReasoningEffort == "" || slices.Contains(selectedModelEntry.ReasoningEfforts, current.DefaultReasoningEffort) {
		effortField.Default = valuePointer(interaction.StringValue(string(current.DefaultReasoningEffort)))
	}
	effortResponse, err := request.Interaction.Request(ctx, interaction.Request{
		Name: setupOperationName, Description: "Select the default reasoning effort.", Fields: []interaction.Field{effortField},
	})
	if err != nil {
		return operation.Result{}, err
	}
	selectedEffort, ok := answerString(effortResponse, "default_reasoning_effort")
	if !ok {
		selectedEffort = string(current.DefaultReasoningEffort)
	}
	updated := Config{DefaultProvider: configuredProvider, DefaultModel: selectedModel, DefaultReasoningEffort: model.ReasoningEffort(selectedEffort)}
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
	effective, err := effectiveConfig(updated, selection.names)
	if err != nil {
		return operation.Result{}, err
	}
	if effective.DefaultModel != "" {
		selection.defaults = effective
		probe := model.Request{Provider: effective.DefaultProvider, Model: effective.DefaultModel, ReasoningEffort: effective.DefaultReasoningEffort}
		if _, err := selection.selectProvider(probe); err != nil {
			return operation.Result{}, fmt.Errorf("invalid model runtime defaults: %w: %w", err, ErrInvalidConfig)
		}
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
	if !utf8.ValidString(config.DefaultProvider) || !utf8.ValidString(config.DefaultModel) || !utf8.ValidString(string(config.DefaultReasoningEffort)) {
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
