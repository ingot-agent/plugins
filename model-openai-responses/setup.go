package openairesponses

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/ingot-agent/ingot-abi/state"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/operation"
)

const (
	setupOperationName  = "config"
	setupOperationGroup = "model-openai-responses"

	apiKeyKeep    = "keep"
	apiKeyReplace = "replace"
	apiKeyClear   = "clear"

	headerValueKeep    = "keep"
	headerValueReplace = "replace"
	headerValueClear   = "clear"
)

// ErrConfigConflict indicates that persisted configuration changed while an
// interactive update was in progress.
var ErrConfigConflict = errors.New("model.openai-responses configuration changed during interaction")

// setupOperation asks the Host for this Plugin's provider list through a
// structured interaction request and persists the answer in its own state
// scope. providers is a repeated object containing a repeated string field,
// which is exactly the shape the nested interaction kinds exist for. The API
// key and additional header values are marked Sensitive, so the Host never
// receives stored secrets back as defaults.
type setupOperation struct {
	scope  state.Scope
	source *providerSource
}

var _ operation.Operation = (*setupOperation)(nil)

func (*setupOperation) Definition() operation.Definition {
	return operation.Definition{
		Name:         setupOperationName,
		Description:  "Review and update the OpenAI Responses model providers.",
		Group:        setupOperationGroup,
		InputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`),
		OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["providers","restart_required"],"properties":{"providers":{"type":"integer","minimum":1},"restart_required":{"type":"boolean"}}}`),
	}
}

func (o *setupOperation) Invoke(ctx context.Context, request operation.Request) (operation.Result, error) {
	if err := ctx.Err(); err != nil {
		return operation.Result{}, err
	}
	if o.scope == nil || o.scope.Dir() == "" {
		return operation.Result{}, fmt.Errorf("model.openai-responses config: state scope is required: %w", ErrInvalidConfig)
	}
	current, err := loadConfig(o.scope.Dir())
	if err != nil {
		return operation.Result{}, err
	}
	providersDefault := providerListValue(current.Providers)
	sourceOptions := []interaction.Option{{Value: "", Label: "New provider"}}
	for _, provider := range current.Providers {
		sourceOptions = append(sourceOptions, interaction.Option{Value: provider.Name, Label: provider.Name})
	}
	headerSources := headerSourceOptions(current.Providers)
	response, err := request.Interaction.Request(ctx, interaction.Request{
		Name:        setupOperationName,
		Description: "One entry per OpenAI Responses provider.",
		Fields: []interaction.Field{
			{Name: "providers", Label: "Providers", Kind: interaction.FieldList, Default: &providersDefault, Element: &interaction.Field{Name: "provider", Kind: interaction.FieldObject, Fields: []interaction.Field{
				{Name: "source", Label: "Existing provider", Description: "Select the provider being edited, or New provider.", Kind: interaction.FieldChoice, Required: true, Options: sourceOptions},
				{Name: "name", Label: "Name", Description: "Stable provider name referenced by model.runtime.", Kind: interaction.FieldString, Required: true},
				{Name: "base_url", Label: "Base URL", Description: "Absolute http/https endpoint.", Kind: interaction.FieldString, Required: true},
				{Name: "reasoning_efforts", Label: "Reasoning efforts", Description: "Reasoning intensities supported by these models.", Kind: interaction.FieldMultiChoice, Options: reasoningEffortOptions()},
				{Name: "api_key_action", Label: "API key", Kind: interaction.FieldChoice, Required: true, Options: []interaction.Option{
					{Value: apiKeyKeep, Label: "Keep"},
					{Value: apiKeyReplace, Label: "Replace"},
					{Value: apiKeyClear, Label: "Clear"},
				}},
				{Name: "api_key", Label: "New API key", Kind: interaction.FieldString, Sensitive: true},
				{Name: "models", Label: "Models", Kind: interaction.FieldList, Element: &interaction.Field{Name: "model", Kind: interaction.FieldString}},
				{Name: "organization", Label: "Organization", Kind: interaction.FieldString},
				{Name: "project", Label: "Project", Kind: interaction.FieldString},
				{Name: "default_headers", Label: "Default headers", Description: "Additional request headers. Remove an entry to delete it; clear stores an empty value.", Kind: interaction.FieldList, Element: &interaction.Field{Name: "header", Kind: interaction.FieldObject, Fields: []interaction.Field{
					{Name: "name", Label: "Name", Kind: interaction.FieldString, Required: true},
					{Name: "source", Label: "Existing header", Description: "Existing names are suggestions because availability depends on the selected provider.", Kind: interaction.FieldString, Required: true, Options: headerSources},
					{Name: "action", Label: "Value action", Kind: interaction.FieldChoice, Required: true, Options: headerValueOptions()},
					{Name: "value", Label: "Value", Kind: interaction.FieldString, Sensitive: true},
				}}},
				{Name: "max_response_bytes", Label: "Maximum response bytes", Description: "Zero selects the built-in default.", Kind: interaction.FieldInteger},
				{Name: "max_error_body_bytes", Label: "Maximum error body bytes", Description: "Zero selects the built-in default.", Kind: interaction.FieldInteger},
				{Name: "max_asset_bytes", Label: "Maximum asset bytes", Description: "Zero selects the built-in default.", Kind: interaction.FieldInteger},
				{Name: "asset_concurrency", Label: "Asset concurrency", Description: "Zero selects the built-in default.", Kind: interaction.FieldInteger},
			}}},
		},
	})
	if err != nil {
		return operation.Result{}, err
	}
	items, ok := answerList(response, "providers")
	if !ok {
		return operation.Result{}, fmt.Errorf("model.openai-responses config: providers are required: %w", ErrInvalidConfig)
	}
	existing := make(map[string]ProviderConfig, len(current.Providers))
	for _, item := range current.Providers {
		existing[item.Name] = item
	}
	providers := make([]ProviderConfig, 0, len(items))
	usedSources := make(map[string]struct{}, len(items))
	for i, item := range items {
		if item.Kind != interaction.ValueObject {
			return operation.Result{}, fmt.Errorf("providers[%d] must be an object: %w", i, ErrInvalidConfig)
		}
		entries := map[string]interaction.Value{}
		for _, entry := range item.Entries {
			entries[entry.Name] = entry.Value
		}
		source, ok := stringEntry(entries, "source")
		if !ok {
			return operation.Result{}, fmt.Errorf("providers[%d].source is required: %w", i, ErrInvalidConfig)
		}
		var previous ProviderConfig
		if source != "" {
			var found bool
			previous, found = existing[source]
			if !found {
				return operation.Result{}, fmt.Errorf("providers[%d].source %q is unknown: %w", i, source, ErrInvalidConfig)
			}
			if _, duplicate := usedSources[source]; duplicate {
				return operation.Result{}, fmt.Errorf("providers[%d].source %q is duplicated: %w", i, source, ErrInvalidConfig)
			}
			usedSources[source] = struct{}{}
		}
		name, ok := stringEntry(entries, "name")
		if !ok || name == "" {
			return operation.Result{}, fmt.Errorf("providers[%d].name is required: %w", i, ErrInvalidConfig)
		}
		baseURL, ok := stringEntry(entries, "base_url")
		if !ok || baseURL == "" {
			return operation.Result{}, fmt.Errorf("providers[%d].base_url is required: %w", i, ErrInvalidConfig)
		}
		candidate := cloneProviderConfig(previous)
		candidate.Name = name
		candidate.BaseURL = baseURL
		if efforts, present := listEntry(entries, "reasoning_efforts"); present {
			candidate.ReasoningEfforts = make([]string, 0, len(efforts))
			for _, effort := range efforts {
				if effort.Kind != interaction.ValueString {
					return operation.Result{}, fmt.Errorf("providers[%d].reasoning_efforts must be strings: %w", i, ErrInvalidConfig)
				}
				candidate.ReasoningEfforts = append(candidate.ReasoningEfforts, effort.String)
			}
		}
		apiKeyAction, ok := stringEntry(entries, "api_key_action")
		if !ok {
			return operation.Result{}, fmt.Errorf("providers[%d].api_key_action is required: %w", i, ErrInvalidConfig)
		}
		switch apiKeyAction {
		case apiKeyKeep:
			if source == "" {
				return operation.Result{}, fmt.Errorf("providers[%d] cannot keep an API key without an existing provider: %w", i, ErrInvalidConfig)
			}
		case apiKeyReplace:
			apiKey, present := stringEntry(entries, "api_key")
			if !present || apiKey == "" {
				return operation.Result{}, fmt.Errorf("providers[%d].api_key is required when replacing: %w", i, ErrInvalidConfig)
			}
			candidate.APIKey = apiKey
		case apiKeyClear:
			candidate.APIKey = ""
		default:
			return operation.Result{}, fmt.Errorf("providers[%d].api_key_action %q is unsupported: %w", i, apiKeyAction, ErrInvalidConfig)
		}
		if organization, present := stringEntry(entries, "organization"); present {
			candidate.Organization = organization
		}
		if project, present := stringEntry(entries, "project"); present {
			candidate.Project = project
		}
		if models, present := listEntry(entries, "models"); present {
			names := make([]string, 0, len(models))
			for _, value := range models {
				if value.Kind != interaction.ValueString {
					return operation.Result{}, fmt.Errorf("providers[%d].models must be strings: %w", i, ErrInvalidConfig)
				}
				names = append(names, value.String)
			}
			candidate.Models = names
		}
		if headers, present := listEntry(entries, "default_headers"); present {
			candidate.DefaultHeaders, err = mergeDefaultHeaders(previous.DefaultHeaders, headers, i)
			if err != nil {
				return operation.Result{}, err
			}
		}
		if value, present := integerEntry(entries, "max_response_bytes"); present {
			candidate.MaxResponseBytes = int(value)
		}
		if value, present := integerEntry(entries, "max_error_body_bytes"); present {
			candidate.MaxErrorBodyBytes = int(value)
		}
		if value, present := integerEntry(entries, "max_asset_bytes"); present {
			candidate.MaxAssetBytes = int(value)
		}
		if value, present := integerEntry(entries, "asset_concurrency"); present {
			candidate.AssetConcurrency = int(value)
		}
		providers = append(providers, candidate)
	}
	updated := Config{Providers: providers}
	// Reuse construction validation so a persisted provider list is always one
	// the Plugin would accept at startup.
	normalized, err := normalizeProviders(updated)
	if err != nil {
		return operation.Result{}, err
	}
	snapshot := o.source.prepare(normalized)
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
	// Publishing cannot fail or be canceled after the configuration is saved.
	o.source.current.Store(snapshot)
	output, err := json.Marshal(map[string]any{"providers": len(updated.Providers), "restart_required": false})
	if err != nil {
		return operation.Result{}, err
	}
	return operation.Result{Output: output}, nil
}

func providerListValue(providers []ProviderConfig) interaction.Value {
	items := make([]interaction.Value, 0, len(providers))
	for _, provider := range providers {
		models := make([]interaction.Value, 0, len(provider.Models))
		for _, model := range provider.Models {
			models = append(models, interaction.StringValue(model))
		}
		headers := defaultHeadersValue(provider.DefaultHeaders)
		efforts := make([]interaction.Value, 0, len(provider.ReasoningEfforts))
		for _, effort := range provider.ReasoningEfforts {
			efforts = append(efforts, interaction.StringValue(effort))
		}
		items = append(items, interaction.ObjectValue([]interaction.Entry{
			{Name: "source", Value: interaction.StringValue(provider.Name)},
			{Name: "name", Value: interaction.StringValue(provider.Name)},
			{Name: "base_url", Value: interaction.StringValue(provider.BaseURL)},
			{Name: "reasoning_efforts", Value: interaction.ListValue(efforts)},
			{Name: "api_key_action", Value: interaction.StringValue(apiKeyKeep)},
			{Name: "models", Value: interaction.ListValue(models)},
			{Name: "organization", Value: interaction.StringValue(provider.Organization)},
			{Name: "project", Value: interaction.StringValue(provider.Project)},
			{Name: "default_headers", Value: headers},
			{Name: "max_response_bytes", Value: interaction.IntegerValue(int64(provider.MaxResponseBytes))},
			{Name: "max_error_body_bytes", Value: interaction.IntegerValue(int64(provider.MaxErrorBodyBytes))},
			{Name: "max_asset_bytes", Value: interaction.IntegerValue(int64(provider.MaxAssetBytes))},
			{Name: "asset_concurrency", Value: interaction.IntegerValue(int64(provider.AssetConcurrency))},
		}))
	}
	return interaction.ListValue(items)
}

func reasoningEffortOptions() []interaction.Option {
	return []interaction.Option{
		{Value: "none", Label: "None"},
		{Value: "minimal", Label: "Minimal"},
		{Value: "low", Label: "Low"},
		{Value: "medium", Label: "Medium"},
		{Value: "high", Label: "High"},
		{Value: "xhigh", Label: "Extra high"},
	}
}

func defaultHeadersValue(headers map[string]string) interaction.Value {
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	items := make([]interaction.Value, 0, len(names))
	for _, name := range names {
		items = append(items, interaction.ObjectValue([]interaction.Entry{
			{Name: "name", Value: interaction.StringValue(name)},
			{Name: "source", Value: interaction.StringValue(name)},
			{Name: "action", Value: interaction.StringValue(headerValueKeep)},
		}))
	}
	return interaction.ListValue(items)
}

func mergeDefaultHeaders(current map[string]string, items []interaction.Value, providerIndex int) (map[string]string, error) {
	updated := make(map[string]string, len(items))
	usedNames := make(map[string]struct{}, len(items))
	usedSources := make(map[string]struct{}, len(items))
	for i, item := range items {
		if item.Kind != interaction.ValueObject {
			return nil, fmt.Errorf("providers[%d].default_headers[%d] must be an object: %w", providerIndex, i, ErrInvalidConfig)
		}
		entries := make(map[string]interaction.Value, len(item.Entries))
		for _, entry := range item.Entries {
			entries[entry.Name] = entry.Value
		}
		name, ok := stringEntry(entries, "name")
		if !ok || name == "" {
			return nil, fmt.Errorf("providers[%d].default_headers[%d].name is required: %w", providerIndex, i, ErrInvalidConfig)
		}
		identity := strings.ToLower(name)
		if _, duplicate := usedNames[identity]; duplicate {
			return nil, fmt.Errorf("providers[%d].default_headers contains duplicate name %q: %w", providerIndex, name, ErrInvalidConfig)
		}
		usedNames[identity] = struct{}{}
		source, ok := stringEntry(entries, "source")
		if !ok {
			return nil, fmt.Errorf("providers[%d].default_headers[%d].source is required: %w", providerIndex, i, ErrInvalidConfig)
		}
		var previous string
		if source != "" {
			var exists bool
			previous, exists = current[source]
			if !exists {
				return nil, fmt.Errorf("providers[%d].default_headers[%d].source %q is unknown: %w", providerIndex, i, source, ErrInvalidConfig)
			}
			if _, duplicate := usedSources[source]; duplicate {
				return nil, fmt.Errorf("providers[%d].default_headers[%d].source %q is duplicated: %w", providerIndex, i, source, ErrInvalidConfig)
			}
			usedSources[source] = struct{}{}
		}
		action, ok := stringEntry(entries, "action")
		if !ok {
			return nil, fmt.Errorf("providers[%d].default_headers[%d].action is required: %w", providerIndex, i, ErrInvalidConfig)
		}
		switch action {
		case headerValueKeep:
			if source == "" {
				return nil, fmt.Errorf("providers[%d].default_headers[%d] cannot keep a value without an existing source: %w", providerIndex, i, ErrInvalidConfig)
			}
			updated[name] = previous
		case headerValueReplace:
			value, supplied := stringEntry(entries, "value")
			if !supplied || value == "" {
				return nil, fmt.Errorf("providers[%d].default_headers[%d].value is required when replacing: %w", providerIndex, i, ErrInvalidConfig)
			}
			updated[name] = value
		case headerValueClear:
			updated[name] = ""
		default:
			return nil, fmt.Errorf("providers[%d].default_headers[%d].action %q is unsupported: %w", providerIndex, i, action, ErrInvalidConfig)
		}
	}
	return updated, nil
}

func headerSourceOptions(providers []ProviderConfig) []interaction.Option {
	names := make(map[string]struct{})
	for _, provider := range providers {
		for name := range provider.DefaultHeaders {
			names[name] = struct{}{}
		}
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	options := make([]interaction.Option, 0, len(ordered))
	for _, name := range ordered {
		options = append(options, interaction.Option{Value: name, Label: name})
	}
	return options
}

func headerValueOptions() []interaction.Option {
	return []interaction.Option{
		{Value: headerValueKeep, Label: "Keep"},
		{Value: headerValueReplace, Label: "Replace"},
		{Value: headerValueClear, Label: "Clear"},
	}
}

func cloneProviderConfig(provider ProviderConfig) ProviderConfig {
	provider.Models = append([]string(nil), provider.Models...)
	if provider.DefaultHeaders != nil {
		headers := make(map[string]string, len(provider.DefaultHeaders))
		for name, value := range provider.DefaultHeaders {
			headers[name] = value
		}
		provider.DefaultHeaders = headers
	}
	return provider
}

func stringEntry(entries map[string]interaction.Value, name string) (string, bool) {
	value, ok := entries[name]
	if !ok || value.Kind != interaction.ValueString {
		return "", false
	}
	return value.String, true
}

func answerList(response interaction.Response, name string) ([]interaction.Value, bool) {
	for _, answer := range response.Values {
		if answer.Name == name && answer.Value.Kind == interaction.ValueList {
			return answer.Value.Items, true
		}
	}
	return nil, false
}

func listEntry(entries map[string]interaction.Value, name string) ([]interaction.Value, bool) {
	value, ok := entries[name]
	if !ok || value.Kind != interaction.ValueList {
		return nil, false
	}
	return value.Items, true
}

func integerEntry(entries map[string]interaction.Value, name string) (int64, bool) {
	value, ok := entries[name]
	if !ok || value.Kind != interaction.ValueInteger {
		return 0, false
	}
	return value.Integer, true
}
