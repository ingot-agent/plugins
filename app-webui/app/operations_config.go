package appcomponent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ingot-agent/ingot-abi/state"
	appbackend "github.com/ingot-agent/plugins/app-webui"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/operation"
)

const setupOperationName = "config"

const setupOperationGroup = "app-webui"

// newConfigOperations returns the Plugin-owned configuration Operations. They
// read and write this Plugin's own state scope; no host-side configuration
// decoding or injection is involved.
func newConfigOperations(scope state.Scope) []operation.Operation {
	return []operation.Operation{&configOperation{scope: scope}}
}

type configOperation struct{ scope state.Scope }

func (*configOperation) Definition() operation.Definition {
	return operation.Definition{
		Name:        setupOperationName,
		Description: "Review and update the app.backend HTTP server configuration.",
		Group:       setupOperationGroup,
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`),
		OutputSchema: json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["address", "replay_capacity", "subscriber_buffer", "heartbeat_interval_seconds", "operation_retention", "max_asset_bytes", "restart_required"],
  "properties": {
    "address": {"type": "string"},
    "replay_capacity": {"type": "integer"},
    "subscriber_buffer": {"type": "integer"},
    "heartbeat_interval_seconds": {"type": "integer"},
    "operation_retention": {"type": "integer"},
    "max_asset_bytes": {"type": "integer"},
    "restart_required": {"type": "boolean"}
  }
}`),
	}
}

func (o *configOperation) Invoke(ctx context.Context, request operation.Request) (operation.Result, error) {
	if err := ctx.Err(); err != nil {
		return operation.Result{}, err
	}
	if o.scope == nil || o.scope.Dir() == "" {
		return operation.Result{}, fmt.Errorf("app.backend config: %w", appbackend.ErrInvalidConfig)
	}
	current, err := appbackend.LoadConfig(o.scope.Dir())
	if err != nil {
		return operation.Result{}, err
	}
	normalized, err := current.Normalize()
	if err != nil {
		return operation.Result{}, err
	}
	address := interaction.StringValue(normalized.Address)
	replayCapacity := interaction.IntegerValue(int64(normalized.ReplayCapacity))
	subscriberBuffer := interaction.IntegerValue(int64(normalized.SubscriberBuffer))
	heartbeatSeconds := interaction.IntegerValue(int64(normalized.Heartbeat / 1e9))
	operationRetention := interaction.IntegerValue(int64(normalized.OperationRetention))
	maxAssetBytes := interaction.IntegerValue(normalized.MaxAssetBytes)
	response, err := request.Interaction.Request(ctx, interaction.Request{
		Name:        setupOperationName,
		Description: "HTTP listener, event replay, operation retention and browser asset limits.",
		Fields: []interaction.Field{
			{Name: "address", Label: "Address", Description: "HTTP bind address, for example 127.0.0.1:7316.", Kind: interaction.FieldString, Required: true, Default: &address},
			{Name: "replay_capacity", Label: "Replay capacity", Kind: interaction.FieldInteger, Required: true, Default: &replayCapacity},
			{Name: "subscriber_buffer", Label: "Subscriber buffer", Kind: interaction.FieldInteger, Required: true, Default: &subscriberBuffer},
			{Name: "heartbeat_interval_seconds", Label: "Heartbeat interval", Description: "Seconds; zero disables heartbeats.", Kind: interaction.FieldInteger, Required: true, Default: &heartbeatSeconds},
			{Name: "operation_retention", Label: "Operation retention", Kind: interaction.FieldInteger, Required: true, Default: &operationRetention},
			{Name: "max_asset_bytes", Label: "Maximum asset bytes", Kind: interaction.FieldInteger, Required: true, Default: &maxAssetBytes},
		},
	})
	if err != nil {
		return operation.Result{}, err
	}
	if value, ok := configString(response, "address"); ok {
		current.Backend.Address = value
	}
	if value, ok := configInteger(response, "replay_capacity"); ok {
		current.Backend.ReplayCapacity = int(value)
	}
	if value, ok := configInteger(response, "subscriber_buffer"); ok {
		current.Backend.SubscriberBuffer = int(value)
	}
	if value, ok := configInteger(response, "heartbeat_interval_seconds"); ok {
		current.Backend.HeartbeatIntervalSeconds = int(value)
	}
	if value, ok := configInteger(response, "operation_retention"); ok {
		current.Backend.OperationRetention = int(value)
	}
	if value, ok := configInteger(response, "max_asset_bytes"); ok {
		current.Backend.MaxAssetBytes = value
	}
	updated, err := current.Normalize()
	if err != nil {
		return operation.Result{}, err
	}
	if err := appbackend.SaveConfig(o.scope.Dir(), current); err != nil {
		return operation.Result{}, err
	}
	// Binding a socket or resizing buffers cannot change in place, so the
	// Plugin reports that a restart is required instead of pretending the
	// running process adopted the new values.
	restartRequired := updated.Address != normalized.Address ||
		updated.ReplayCapacity != normalized.ReplayCapacity ||
		updated.SubscriberBuffer != normalized.SubscriberBuffer ||
		updated.Heartbeat != normalized.Heartbeat ||
		updated.OperationRetention != normalized.OperationRetention ||
		updated.MaxAssetBytes != normalized.MaxAssetBytes
	output, err := json.Marshal(map[string]any{
		"address":                    updated.Address,
		"replay_capacity":            updated.ReplayCapacity,
		"subscriber_buffer":          updated.SubscriberBuffer,
		"heartbeat_interval_seconds": int(updated.Heartbeat / 1e9),
		"operation_retention":        updated.OperationRetention,
		"max_asset_bytes":            updated.MaxAssetBytes,
		"restart_required":           restartRequired,
	})
	if err != nil {
		return operation.Result{}, err
	}
	return operation.Result{Output: output}, nil
}

func configString(response interaction.Response, name string) (string, bool) {
	for _, answer := range response.Values {
		if answer.Name == name && answer.Value.Kind == interaction.ValueString {
			return answer.Value.String, true
		}
	}
	return "", false
}

func configInteger(response interaction.Response, name string) (int64, bool) {
	for _, answer := range response.Values {
		if answer.Name == name && answer.Value.Kind == interaction.ValueInteger {
			return answer.Value.Integer, true
		}
	}
	return 0, false
}

var _ operation.Operation = (*configOperation)(nil)
