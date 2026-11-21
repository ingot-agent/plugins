package toolweb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/ingot-agent/ingot-abi/state"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/operation"
)

const (
	setupOperationName  = "config"
	setupOperationGroup = "tool-web"

	apiKeyKeep    = "keep"
	apiKeyReplace = "replace"
	apiKeyClear   = "clear"
)

// setupOperation collects this Plugin's configuration through structured
// Interaction, validates it, persists it atomically in the Plugin-owned state
// scope, and publishes a new immutable snapshot to live tool calls.
type setupOperation struct {
	scope         state.Scope
	configuration *configState
}

var _ operation.Operation = (*setupOperation)(nil)

func (*setupOperation) Definition() operation.Definition {
	return operation.Definition{
		Name:         setupOperationName,
		Description:  "Review and update DeepSeek Search and Fetch API settings.",
		Group:        setupOperationGroup,
		InputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`),
		OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["configured","api_key_source","restart_required"],"properties":{"configured":{"type":"boolean"},"api_key_source":{"type":"string","enum":["config","environment","none"]},"restart_required":{"type":"boolean"}}}`),
	}
}

func (o *setupOperation) Invoke(ctx context.Context, request operation.Request) (operation.Result, error) {
	if ctx == nil {
		return operation.Result{}, errors.New("tool-web.config: nil context")
	}
	if err := ctx.Err(); err != nil {
		return operation.Result{}, err
	}
	if o == nil || o.configuration == nil || isNil(o.scope) || strings.TrimSpace(o.scope.Dir()) == "" {
		return operation.Result{}, fmt.Errorf("tool-web.config: state scope is required: %w", ErrInvalidConfig)
	}
	if isNil(request.Interaction) {
		return operation.Result{}, fmt.Errorf("tool-web.config: interaction channel is required: %w", ErrInvalidConfig)
	}
	dir := o.scope.Dir()
	current, err := loadConfig(dir)
	if err != nil {
		return operation.Result{}, err
	}
	currentNormalized, err := normalizeConfig(current)
	if err != nil {
		return operation.Result{}, err
	}
	currentEffective, err := resolveEffective(currentNormalized)
	if err != nil {
		return operation.Result{}, err
	}

	defaultAction := apiKeyKeep
	if currentEffective.apiKeySource == "none" {
		defaultAction = apiKeyReplace
	}
	apiKeyAction := interaction.StringValue(defaultAction)
	baseURL := interaction.StringValue(currentNormalized.baseURL)
	timeout := interaction.IntegerValue(int64(currentNormalized.requestTimeout.Seconds()))
	maxResponseBytes := interaction.IntegerValue(currentNormalized.maxResponseBytes)
	response, err := request.Interaction.Request(ctx, interaction.Request{
		Name:        setupOperationName,
		Description: "DeepSeek Search and Fetch API settings.",
		Fields: []interaction.Field{
			{
				Name:        "api_key_action",
				Label:       "API key",
				Description: apiKeyDescription(currentEffective),
				Kind:        interaction.FieldChoice,
				Required:    true,
				Default:     &apiKeyAction,
				Options: []interaction.Option{
					{Value: apiKeyKeep, Label: "Keep current key"},
					{Value: apiKeyReplace, Label: "Replace key"},
					{Value: apiKeyClear, Label: "Clear stored key / use environment"},
				},
			},
			{
				Name:        "base_url",
				Label:       "API base URL",
				Description: "DeepSeek API root, for example https://api.deepseek.com. A /v1 suffix is accepted.",
				Kind:        interaction.FieldString,
				Required:    true,
				Default:     &baseURL,
			},
			{
				Name:        "request_timeout_seconds",
				Label:       "Request timeout (seconds)",
				Description: "Total time budget for one tool call, including retries.",
				Kind:        interaction.FieldInteger,
				Default:     &timeout,
			},
			{
				Name:        "max_response_bytes",
				Label:       "Maximum response bytes",
				Description: "Bounded DeepSeek response body. Zero selects the default.",
				Kind:        interaction.FieldInteger,
				Default:     &maxResponseBytes,
			},
		},
	})
	if err != nil {
		return operation.Result{}, err
	}

	updated := current
	action := apiKeyKeep
	if value, ok := answerString(response, "api_key_action"); ok && value != "" {
		action = value
	}
	switch action {
	case apiKeyKeep:
	case apiKeyReplace:
		keyResponse, err := request.Interaction.Request(ctx, interaction.Request{
			Name:        setupOperationName + ".api-key",
			Description: "DeepSeek API key used for the Search and Fetch APIs.",
			Fields: []interaction.Field{{
				Name:      "api_key",
				Label:     "DeepSeek API key",
				Kind:      interaction.FieldString,
				Required:  true,
				Sensitive: true,
			}},
		})
		if err != nil {
			return operation.Result{}, err
		}
		apiKey, ok := answerString(keyResponse, "api_key")
		if !ok || strings.TrimSpace(apiKey) == "" {
			return operation.Result{}, fmt.Errorf("api_key is required when replacing: %w", ErrInvalidConfig)
		}
		updated.APIKey = apiKey
	case apiKeyClear:
		updated.APIKey = ""
	default:
		return operation.Result{}, fmt.Errorf("api_key_action %q is unsupported: %w", action, ErrInvalidConfig)
	}
	if value, ok := answerString(response, "base_url"); ok {
		updated.BaseURL = value
	}
	if value, ok := answerInteger(response, "request_timeout_seconds"); ok {
		updated.RequestTimeoutSeconds = int(value)
	}
	if value, ok := answerInteger(response, "max_response_bytes"); ok {
		updated.MaxResponseBytes = int(value)
	}

	normalized, err := normalizeConfig(updated)
	if err != nil {
		return operation.Result{}, err
	}
	effective, err := resolveEffective(normalized)
	if err != nil {
		return operation.Result{}, err
	}
	canonical := canonicalConfig(normalized)

	configCommitMu.Lock()
	defer configCommitMu.Unlock()
	latest, err := loadConfig(dir)
	if err != nil {
		return operation.Result{}, err
	}
	if !reflect.DeepEqual(latest, current) {
		return operation.Result{}, ErrConfigConflict
	}
	if err := ctx.Err(); err != nil {
		return operation.Result{}, err
	}
	if err := saveConfig(dir, canonical); err != nil {
		return operation.Result{}, err
	}
	o.configuration.current.Store(&effective)
	output, err := json.Marshal(struct {
		Configured      bool   `json:"configured"`
		APIKeySource    string `json:"api_key_source"`
		RestartRequired bool   `json:"restart_required"`
	}{Configured: effective.configured(), APIKeySource: effective.apiKeySource, RestartRequired: false})
	if err != nil {
		return operation.Result{}, err
	}
	return operation.Result{Output: output}, nil
}

func apiKeyDescription(configuration effectiveConfig) string {
	switch configuration.apiKeySource {
	case "config":
		return "A key is stored in this plugin's state. Keep it, replace it, or clear it."
	case "environment":
		return "Using DEEPSEEK_API_KEY from the process environment. Replace stores a key here; clear keeps using the environment."
	default:
		return "No key is configured. Choose Replace, or set DEEPSEEK_API_KEY."
	}
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
