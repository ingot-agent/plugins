package httpdefault

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

var ErrConfigConflict = errors.New("http.default configuration changed during interaction")

const (
	setupOperationName  = "config"
	setupOperationGroup = "http-default"
	proxyURLKeep        = "keep"
	proxyURLReplace     = "replace"
)

// setupOperation asks the Host for this Plugin's configuration through a
// structured interaction request and persists the answer in its own state
// scope. The Plugin owns validation and persistence; the Host never decodes
// plugin configuration.
type setupOperation struct {
	scope  state.Scope
	client *clientState
}

var _ operation.Operation = (*setupOperation)(nil)

func (*setupOperation) Definition() operation.Definition {
	return operation.Definition{
		Name:         setupOperationName,
		Description:  "HTTP transport and proxy settings.",
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
		return operation.Result{}, fmt.Errorf("http.default.config: state scope is required: %w", ErrInvalidConfig)
	}
	current, err := loadConfig(o.scope.Dir())
	if err != nil {
		return operation.Result{}, err
	}
	currentEffective, err := effectiveConfiguration(current)
	if err != nil {
		return operation.Result{}, err
	}
	response, err := request.Interaction.Request(ctx, interaction.Request{
		Name:        setupOperationName,
		Description: "HTTP transport and proxy settings.",
		Fields: []interaction.Field{
			{Name: "proxy_mode", Label: "Proxy Mode", Kind: interaction.FieldChoice, Required: true, Default: valuePointer(interaction.StringValue(currentEffective.proxyMode)), Options: []interaction.Option{
				{Value: "environment", Label: "Environment"},
				{Value: "direct", Label: "Direct"},
				{Value: "url", Label: "Proxy URL"},
			}},
			{Name: "max_idle_conns", Label: "Max Idle Conns", Kind: interaction.FieldInteger, Required: false, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: int64(current.MaxIdleConns)}},
			{Name: "max_idle_conns_per_host", Label: "Max Idle Conns Per Host", Kind: interaction.FieldInteger, Required: false, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: int64(current.MaxIdleConnsPerHost)}},
			{Name: "idle_conn_timeout_seconds", Label: "Idle Conn Timeout Seconds", Kind: interaction.FieldInteger, Required: false, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: int64(current.IdleConnTimeoutSeconds)}},
			{Name: "tls_handshake_timeout_seconds", Label: "Tls Handshake Timeout Seconds", Kind: interaction.FieldInteger, Required: false, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: int64(current.TLSHandshakeTimeoutSeconds)}},
		},
	})
	if err != nil {
		return operation.Result{}, err
	}
	// Start from the persisted configuration and overlay only the answers the
	// Host supplied, so an untouched field keeps its current value.
	updated := current
	if v, ok := answerString(response, "proxy_mode"); ok {
		updated.ProxyMode = v
	} else {
		return operation.Result{}, fmt.Errorf("proxy_mode is required: %w", ErrInvalidConfig)
	}
	if v, ok := answerInteger(response, "max_idle_conns"); ok {
		updated.MaxIdleConns = int(v)
	}
	if v, ok := answerInteger(response, "max_idle_conns_per_host"); ok {
		updated.MaxIdleConnsPerHost = int(v)
	}
	if v, ok := answerInteger(response, "idle_conn_timeout_seconds"); ok {
		updated.IdleConnTimeoutSeconds = int(v)
	}
	if v, ok := answerInteger(response, "tls_handshake_timeout_seconds"); ok {
		updated.TLSHandshakeTimeoutSeconds = int(v)
	}
	if updated.ProxyMode == "url" {
		action := proxyURLReplace
		if currentEffective.proxyMode == "url" && current.ProxyURL != "" {
			actionResponse, err := request.Interaction.Request(ctx, interaction.Request{
				Name:        setupOperationName + ".proxy-url-action",
				Description: "Choose whether to retain or replace the stored proxy endpoint.",
				Fields: []interaction.Field{{
					Name: "proxy_url_action", Label: "Proxy URL", Kind: interaction.FieldChoice, Required: true,
					Default: valuePointer(interaction.StringValue(proxyURLKeep)),
					Options: []interaction.Option{{Value: proxyURLKeep, Label: "Keep"}, {Value: proxyURLReplace, Label: "Replace"}},
				}},
			})
			if err != nil {
				return operation.Result{}, err
			}
			selected, ok := answerString(actionResponse, "proxy_url_action")
			if !ok || (selected != proxyURLKeep && selected != proxyURLReplace) {
				return operation.Result{}, fmt.Errorf("proxy_url_action is invalid: %w", ErrInvalidConfig)
			}
			action = selected
		}
		if action == proxyURLReplace {
			proxyResponse, err := request.Interaction.Request(ctx, interaction.Request{
				Name:        setupOperationName + ".proxy-url",
				Description: "Proxy endpoint used for HTTP requests.",
				Fields:      []interaction.Field{{Name: "proxy_url", Label: "Proxy URL", Kind: interaction.FieldString, Required: true, Sensitive: true}},
			})
			if err != nil {
				return operation.Result{}, err
			}
			proxyURL, ok := answerString(proxyResponse, "proxy_url")
			if !ok || proxyURL == "" {
				return operation.Result{}, fmt.Errorf("proxy_url is required when replacing: %w", ErrInvalidConfig)
			}
			updated.ProxyURL = proxyURL
		}
	} else {
		updated.ProxyURL = ""
	}
	normalized, err := normalizeConfig(updated)
	if err != nil {
		return operation.Result{}, err
	}
	candidate, candidateTransport := newHTTPClient(normalized)
	published := false
	defer func() {
		if !published {
			candidateTransport.CloseIdleConnections()
		}
	}()
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
	o.client.mu.Lock()
	if err := ctx.Err(); err != nil {
		o.client.mu.Unlock()
		return operation.Result{}, err
	}
	if err := saveConfig(o.scope.Dir(), updated); err != nil {
		o.client.mu.Unlock()
		return operation.Result{}, err
	}
	previousTransport := o.client.transport
	o.client.client = candidate
	o.client.transport = candidateTransport
	o.client.mu.Unlock()
	published = true
	previousTransport.CloseIdleConnections()
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
