package appcomponent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"

	"github.com/ingot-agent/ingot-abi/state"
	appbackend "github.com/ingot-agent/plugins/app-webui"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/operation"
)

const setupOperationName = "config"

const setupOperationGroup = "app-webui"

var (
	configCommitMu    sync.Mutex
	errConfigConflict = errors.New("app.backend configuration changed during interaction")
)

// newConfigOperations returns the Plugin-owned configuration Operations. They
// read and write this Plugin's own state scope; no host-side configuration
// decoding or injection is involved.
func newConfigOperations(scope state.Scope, active appbackend.NormalizedBackendConfig, workspaces *workspaceState, publish func(appbackend.WorkspaceState)) []operation.Operation {
	return []operation.Operation{&configOperation{scope: scope, active: active, workspaces: workspaces, publish: publish}}
}

type configOperation struct {
	scope      state.Scope
	active     appbackend.NormalizedBackendConfig
	workspaces *workspaceState
	// publish tells open clients about a new fallback Workspace, which no other
	// event reports because it is server state rather than Session state.
	publish func(appbackend.WorkspaceState)
}

func (*configOperation) Definition() operation.Definition {
	return operation.Definition{
		Name:        setupOperationName,
		Description: "Review and update the app.backend HTTP server, text editor and default workspace configuration.",
		Group:       setupOperationGroup,
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`),
		OutputSchema: json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["address", "replay_capacity", "subscriber_buffer", "heartbeat_interval_seconds", "operation_retention", "max_asset_bytes", "text_editor_command", "default_workspace", "restart_required"],
  "properties": {
    "address": {"type": "string"},
    "replay_capacity": {"type": "integer"},
    "subscriber_buffer": {"type": "integer"},
    "heartbeat_interval_seconds": {"type": "integer"},
    "operation_retention": {"type": "integer"},
    "max_asset_bytes": {"type": "integer"},
    "text_editor_command": {"type": "string"},
    "default_workspace": {"type": "string"},
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
	currentBeforeUpdate := current
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
	textEditorCommand := interaction.StringValue(normalized.TextEditorCommand)
	defaultWorkspace := interaction.StringValue(normalized.DefaultWorkspace)
	response, err := request.Interaction.Request(ctx, interaction.Request{
		Name:        setupOperationName,
		Description: configDescription(o.workspaces.get()),
		Fields: []interaction.Field{
			{Name: "address", Label: "Address", Description: "HTTP bind address, for example 127.0.0.1:7316.", Kind: interaction.FieldString, Required: true, Default: &address},
			{Name: "replay_capacity", Label: "Replay capacity", Kind: interaction.FieldInteger, Required: true, Default: &replayCapacity},
			{Name: "subscriber_buffer", Label: "Subscriber buffer", Kind: interaction.FieldInteger, Required: true, Default: &subscriberBuffer},
			{Name: "heartbeat_interval_seconds", Label: "Heartbeat interval", Description: "Seconds; zero selects the built-in default.", Kind: interaction.FieldInteger, Required: true, Default: &heartbeatSeconds},
			{Name: "operation_retention", Label: "Operation retention", Kind: interaction.FieldInteger, Required: true, Default: &operationRetention},
			{Name: "max_asset_bytes", Label: "Maximum asset bytes", Kind: interaction.FieldInteger, Required: true, Default: &maxAssetBytes},
			{Name: "text_editor_command", Label: "Text editor command", Description: `Command used to open files, for example code --reuse-window "${file_path}". Include ${file_path} in an argument; quote executable paths containing spaces. Empty uses the system text application. Saved changes apply on the next open.`, Kind: interaction.FieldString, Default: &textEditorCommand, Options: textEditorSuggestions()},
			{Name: "default_workspace", Label: "Default workspace", Description: "Absolute directory used by conversations that have no workspace of their own. It is created when missing. Empty uses the built-in workspace inside the Plugin state directory. Saved changes apply immediately; existing conversations keep the directory they were bound to.", Kind: interaction.FieldString, Default: &defaultWorkspace},
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
	if value, ok := configString(response, "text_editor_command"); ok {
		current.Backend.TextEditorCommand = value
	}
	if value, ok := configString(response, "default_workspace"); ok {
		current.Backend.DefaultWorkspace = strings.TrimSpace(value)
	}
	updated, err := current.Normalize()
	if err != nil {
		return operation.Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return operation.Result{}, err
	}
	configCommitMu.Lock()
	defer configCommitMu.Unlock()
	latest, err := appbackend.LoadConfig(o.scope.Dir())
	if err != nil {
		return operation.Result{}, err
	}
	if latest != currentBeforeUpdate {
		return operation.Result{}, errConfigConflict
	}
	// Resolve and create the directory before persisting, so a path the server
	// cannot use never reaches the configuration file and the next startup is
	// never the thing that discovers a typo.
	resolvedWorkspace, err := prepareDefaultWorkspace(o.scope.Dir(), updated.DefaultWorkspace)
	if err != nil {
		return operation.Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return operation.Result{}, err
	}
	if err := appbackend.SaveConfig(o.scope.Dir(), current); err != nil {
		return operation.Result{}, err
	}
	// The editor command is loaded for each open action and the default
	// workspace is read per request. Server settings still require a restart,
	// including any changes saved in a previous invocation.
	serverSettings := updated
	serverSettings.TextEditorCommand = o.active.TextEditorCommand
	serverSettings.DefaultWorkspace = o.active.DefaultWorkspace
	restartRequired := serverSettings != o.active
	o.workspaces.set(resolvedWorkspace)
	if o.publish != nil {
		o.publish(appbackend.WorkspaceState{DefaultPath: resolvedWorkspace})
	}
	output, err := json.Marshal(map[string]any{
		"address":                    updated.Address,
		"replay_capacity":            updated.ReplayCapacity,
		"subscriber_buffer":          updated.SubscriberBuffer,
		"heartbeat_interval_seconds": int(updated.Heartbeat / 1e9),
		"operation_retention":        updated.OperationRetention,
		"max_asset_bytes":            updated.MaxAssetBytes,
		"text_editor_command":        updated.TextEditorCommand,
		"default_workspace":          updated.DefaultWorkspace,
		"restart_required":           restartRequired,
	})
	if err != nil {
		return operation.Result{}, err
	}
	return operation.Result{Output: output}, nil
}

// configDescription names the directory new conversations fall back to, so the
// form shows where they will land without the reader opening the state scope.
func configDescription(workspace string) string {
	description := "HTTP listener, event replay, operation retention, browser asset limits, text editor and default workspace."
	if workspace != "" {
		description += fmt.Sprintf(" Conversations without their own workspace currently use %s.", workspace)
	}
	return description
}

func textEditorSuggestions() []interaction.Option {
	options := []interaction.Option{{Value: "", Label: "System text application"}}
	if runtime.GOOS == "windows" {
		return append(options, interaction.Option{Value: `notepad.exe "${file_path}"`, Label: "Notepad"})
	}
	return append(options,
		interaction.Option{Value: `code --reuse-window "${file_path}"`, Label: "VS Code"},
		interaction.Option{Value: `cursor --reuse-window "${file_path}"`, Label: "Cursor"},
		interaction.Option{Value: `subl "${file_path}"`, Label: "Sublime Text"},
	)
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
