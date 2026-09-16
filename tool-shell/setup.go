package toolshell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"

	"github.com/ingot-agent/ingot-abi/state"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/operation"
)

const (
	setupOperationName  = "config"
	setupOperationGroup = "tool-shell"

	inheritModeAll      = "all"
	inheritModeNone     = "none"
	inheritModeSelected = "selected"

	environmentValueKeep    = "keep"
	environmentValueReplace = "replace"
	environmentValueClear   = "clear"
)

// ErrConfigConflict indicates that persisted configuration changed while an
// interactive update was in progress.
var ErrConfigConflict = errors.New("tool.shell configuration changed during interaction")

// setupOperation asks the Host for this Plugin's configuration through a
// structured interaction request and persists the answer in its own state
// scope. environment and inherit_env use the nested and repeated field kinds
// because they are maps and lists rather than scalar values.
type setupOperation struct {
	scope  state.Scope
	active normalizedConfig
}

var _ operation.Operation = (*setupOperation)(nil)

func (*setupOperation) Definition() operation.Definition {
	return operation.Definition{
		Name:         setupOperationName,
		Description:  "Review and update the tool.shell execution boundary.",
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
		return operation.Result{}, fmt.Errorf("tool.shell config: state scope is required: %w", ErrInvalidConfig)
	}
	current, err := loadConfig(o.scope.Dir())
	if err != nil {
		return operation.Result{}, err
	}
	timeoutDefault := int64(current.TimeoutSeconds)
	if timeoutDefault == 0 {
		timeoutDefault = defaultTimeoutSeconds
	}
	maxOutputDefault := int64(current.MaxOutputBytes)
	if maxOutputDefault == 0 {
		maxOutputDefault = defaultMaxOutputBytes
	}
	environmentDefault := environmentNamesValue(current.Environment)
	inheritMode := inheritModeSelected
	if current.InheritEnv == nil {
		inheritMode = inheritModeAll
	} else if len(current.InheritEnv) == 0 {
		inheritMode = inheritModeNone
	}
	response, err := request.Interaction.Request(ctx, interaction.Request{
		Name:        setupOperationName,
		Description: "Shell execution limits, environment and inherited variables.",
		Fields: []interaction.Field{
			{Name: "shell", Label: "Shell", Description: "Absolute shell path; empty resolves a platform default.", Kind: interaction.FieldString, Default: stringValue(current.Shell)},
			{Name: "timeout_seconds", Label: "Timeout seconds", Kind: interaction.FieldInteger, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: timeoutDefault}},
			{Name: "max_output_bytes", Label: "Max output bytes", Kind: interaction.FieldInteger, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: maxOutputDefault}},
			{Name: "environment", Label: "Environment", Description: "Explicit environment entries. Remove an entry to delete it.", Kind: interaction.FieldList, Default: &environmentDefault, Element: &interaction.Field{Name: "entry", Kind: interaction.FieldObject, Fields: []interaction.Field{
				{Name: "name", Kind: interaction.FieldString, Required: true},
				{Name: "source", Label: "Existing entry", Kind: interaction.FieldChoice, Required: true, Options: environmentSourceOptions(current.Environment)},
				{Name: "action", Label: "Value action", Kind: interaction.FieldChoice, Required: true, Options: environmentValueOptions()},
				{Name: "value", Kind: interaction.FieldString, Sensitive: true},
			}}},
			{Name: "inherit_mode", Label: "Parent environment", Description: "Inherit all, none, or only selected parent variables.", Kind: interaction.FieldChoice, Required: true, Default: valuePointer(interaction.StringValue(inheritMode)), Options: []interaction.Option{
				{Value: inheritModeAll, Label: "All"},
				{Value: inheritModeNone, Label: "None"},
				{Value: inheritModeSelected, Label: "Selected"},
			}},
			{Name: "inherit_env", Label: "Selected variables", Description: "Parent variable names used when parent environment is selected.", Kind: interaction.FieldList, Element: &interaction.Field{Name: "name", Kind: interaction.FieldString}, Default: valuePointer(interaction.ListValue(stringItems(current.InheritEnv)))},
		},
	})
	if err != nil {
		return operation.Result{}, err
	}
	updated := current
	if v, ok := answerString(response, "shell"); ok {
		updated.Shell = v
	}
	if v, ok := answerInteger(response, "timeout_seconds"); ok {
		updated.TimeoutSeconds = int(v)
	}
	if v, ok := answerInteger(response, "max_output_bytes"); ok {
		updated.MaxOutputBytes = int(v)
	}
	if v, ok := answerList(response, "environment"); ok {
		updated.Environment, err = mergeEnvironment(current.Environment, v)
		if err != nil {
			return operation.Result{}, err
		}
	}
	inheritMode, ok := answerString(response, "inherit_mode")
	if !ok {
		return operation.Result{}, fmt.Errorf("inherit_mode is required: %w", ErrInvalidConfig)
	}
	switch inheritMode {
	case inheritModeAll:
		updated.InheritEnv = nil
	case inheritModeNone:
		updated.InheritEnv = []string{}
	case inheritModeSelected:
		v, ok := answerList(response, "inherit_env")
		if !ok || len(v) == 0 {
			return operation.Result{}, fmt.Errorf("inherit_env requires at least one name in selected mode: %w", ErrInvalidConfig)
		}
		names := make([]string, 0, len(v))
		for _, item := range v {
			if item.Kind != interaction.ValueString {
				return operation.Result{}, fmt.Errorf("inherit_env entries must be strings: %w", ErrInvalidConfig)
			}
			names = append(names, item.String)
		}
		updated.InheritEnv = names
	default:
		return operation.Result{}, fmt.Errorf("unknown inherit_mode %q: %w", inheritMode, ErrInvalidConfig)
	}
	normalized, err := normalizeConfig(updated)
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
	output, err := json.Marshal(map[string]any{"restart_required": !reflect.DeepEqual(normalized, o.active)})
	if err != nil {
		return operation.Result{}, err
	}
	return operation.Result{Output: output}, nil
}

func environmentNamesValue(environment map[string]string) interaction.Value {
	items := make([]interaction.Value, 0, len(environment))
	for _, name := range sortedKeys(environment) {
		items = append(items, interaction.ObjectValue([]interaction.Entry{
			{Name: "name", Value: interaction.StringValue(name)},
			{Name: "source", Value: interaction.StringValue(name)},
			{Name: "action", Value: interaction.StringValue(environmentValueKeep)},
		}))
	}
	return interaction.ListValue(items)
}

func mergeEnvironment(current map[string]string, items []interaction.Value) (map[string]string, error) {
	updated := make(map[string]string, len(items))
	usedSources := make(map[string]struct{}, len(items))
	for i, item := range items {
		if item.Kind != interaction.ValueObject {
			return nil, fmt.Errorf("environment[%d] must be an object: %w", i, ErrInvalidConfig)
		}
		entries := make(map[string]interaction.Value, len(item.Entries))
		for _, entry := range item.Entries {
			entries[entry.Name] = entry.Value
		}
		name, ok := entries["name"]
		if !ok || name.Kind != interaction.ValueString || name.String == "" {
			return nil, fmt.Errorf("environment[%d] requires a name: %w", i, ErrInvalidConfig)
		}
		if _, duplicate := updated[name.String]; duplicate {
			return nil, fmt.Errorf("duplicate environment name %q: %w", name.String, ErrInvalidConfig)
		}
		source, ok := entries["source"]
		if !ok || source.Kind != interaction.ValueString {
			return nil, fmt.Errorf("environment[%d].source is required: %w", i, ErrInvalidConfig)
		}
		var previous string
		if source.String != "" {
			var exists bool
			previous, exists = current[source.String]
			if !exists {
				return nil, fmt.Errorf("environment[%d].source %q is unknown: %w", i, source.String, ErrInvalidConfig)
			}
			if _, duplicate := usedSources[source.String]; duplicate {
				return nil, fmt.Errorf("environment[%d].source %q is duplicated: %w", i, source.String, ErrInvalidConfig)
			}
			usedSources[source.String] = struct{}{}
		}
		action, ok := entries["action"]
		if !ok || action.Kind != interaction.ValueString {
			return nil, fmt.Errorf("environment[%d].action is required: %w", i, ErrInvalidConfig)
		}
		switch action.String {
		case environmentValueKeep:
			if source.String == "" {
				return nil, fmt.Errorf("environment[%d] cannot keep a value without an existing source: %w", i, ErrInvalidConfig)
			}
			updated[name.String] = previous
		case environmentValueReplace:
			value, supplied := entries["value"]
			if !supplied || value.Kind != interaction.ValueString {
				return nil, fmt.Errorf("environment[%d].value is required when replacing: %w", i, ErrInvalidConfig)
			}
			updated[name.String] = value.String
		case environmentValueClear:
			updated[name.String] = ""
		default:
			return nil, fmt.Errorf("environment[%d].action %q is unsupported: %w", i, action.String, ErrInvalidConfig)
		}
	}
	return updated, nil
}

func environmentSourceOptions(environment map[string]string) []interaction.Option {
	options := []interaction.Option{{Value: "", Label: "New entry"}}
	for _, name := range sortedKeys(environment) {
		options = append(options, interaction.Option{Value: name, Label: name})
	}
	return options
}

func environmentValueOptions() []interaction.Option {
	return []interaction.Option{
		{Value: environmentValueKeep, Label: "Keep"},
		{Value: environmentValueReplace, Label: "Replace"},
		{Value: environmentValueClear, Label: "Clear"},
	}
}

func valuePointer(value interaction.Value) *interaction.Value { return &value }

func stringValue(value string) *interaction.Value {
	if value == "" {
		return nil
	}
	result := interaction.StringValue(value)
	return &result
}

func stringItems(values []string) []interaction.Value {
	items := make([]interaction.Value, 0, len(values))
	for _, value := range values {
		items = append(items, interaction.StringValue(value))
	}
	return items
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
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

func answerList(response interaction.Response, name string) ([]interaction.Value, bool) {
	for _, answer := range response.Values {
		if answer.Name == name && answer.Value.Kind == interaction.ValueList {
			return answer.Value.Items, true
		}
	}
	return nil, false
}
