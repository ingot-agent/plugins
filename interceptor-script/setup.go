package script

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
	setupOperationGroup = "interceptor-script"

	environmentValueKeep    = "keep"
	environmentValueReplace = "replace"
	environmentValueClear   = "clear"
)

// ErrConfigConflict indicates that persisted configuration changed while an
// interactive update was in progress.
var ErrConfigConflict = errors.New("interceptor.script configuration changed during interaction")

// setupOperation asks the Host for this Plugin's hook declarations through a
// structured interaction request and persists the answer in its own state
// scope. hooks is a repeated object containing a repeated string field and a
// map, which is exactly the shape the nested interaction kinds exist for.
type setupOperation struct {
	scope  state.Scope
	active []normalizedHook
}

var _ operation.Operation = (*setupOperation)(nil)

func (*setupOperation) Definition() operation.Definition {
	return operation.Definition{
		Name:         setupOperationName,
		Description:  "Review and update the script hook declarations.",
		Group:        setupOperationGroup,
		InputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`),
		OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["hooks","restart_required"],"properties":{"hooks":{"type":"integer","minimum":0},"restart_required":{"type":"boolean"}}}`),
	}
}

func (o *setupOperation) Invoke(ctx context.Context, request operation.Request) (operation.Result, error) {
	if err := ctx.Err(); err != nil {
		return operation.Result{}, err
	}
	if o.scope == nil || o.scope.Dir() == "" {
		return operation.Result{}, fmt.Errorf("interceptor.script config: state scope is required: %w", ErrInvalidConfig)
	}
	current, err := loadConfig(o.scope.Dir())
	if err != nil {
		return operation.Result{}, err
	}
	hooksDefault := scriptHooksValue(current.Hooks)
	hookSources := hookSourceOptions(current.Hooks)
	environmentSources := environmentSourceOptions(current.Hooks)
	response, err := request.Interaction.Request(ctx, interaction.Request{
		Name:        setupOperationName,
		Description: "One entry per hook; executable must be an absolute path.",
		Fields: []interaction.Field{
			{Name: "hooks", Label: "Hooks", Kind: interaction.FieldList, Default: &hooksDefault, Element: &interaction.Field{Name: "hook", Kind: interaction.FieldObject, Fields: []interaction.Field{
				{Name: "source", Label: "Existing hook", Description: "Select the hook being edited, or New hook.", Kind: interaction.FieldChoice, Required: true, Options: hookSources},
				{Name: "name", Label: "Name", Kind: interaction.FieldString, Required: true},
				{Name: "target", Label: "Target", Kind: interaction.FieldChoice, Required: true, Options: []interaction.Option{
					{Value: "tool", Label: "Tool"},
					{Value: "model", Label: "Model"},
					{Value: "model-stream", Label: "Model stream"},
					{Value: "agent", Label: "Agent"},
				}},
				{Name: "executable", Label: "Executable", Description: "Absolute path to a regular file.", Kind: interaction.FieldString, Required: true},
				{Name: "args", Label: "Arguments", Kind: interaction.FieldList, Element: &interaction.Field{Name: "arg", Kind: interaction.FieldString}},
				{Name: "timeout_seconds", Label: "Timeout seconds", Kind: interaction.FieldInteger},
				{Name: "max_output_bytes", Label: "Max output bytes", Kind: interaction.FieldInteger},
				{Name: "environment", Label: "Environment", Description: "Remove an entry to delete it.", Kind: interaction.FieldList, Element: &interaction.Field{Name: "entry", Kind: interaction.FieldObject, Fields: []interaction.Field{
					{Name: "name", Label: "Name", Kind: interaction.FieldString, Required: true},
					{Name: "source", Label: "Existing entry", Description: "Existing names are suggestions because availability depends on the selected hook.", Kind: interaction.FieldString, Required: true, Options: environmentSources},
					{Name: "action", Label: "Value action", Kind: interaction.FieldChoice, Required: true, Options: environmentValueOptions()},
					{Name: "value", Label: "Value", Kind: interaction.FieldString, Sensitive: true},
				}}},
			}}},
		},
	})
	if err != nil {
		return operation.Result{}, err
	}
	items, ok := answerList(response, "hooks")
	if !ok {
		return operation.Result{}, fmt.Errorf("interceptor.script config: hooks are required: %w", ErrInvalidConfig)
	}
	hooks := make([]Hook, 0, len(items))
	currentByName := make(map[string]Hook, len(current.Hooks))
	for _, hook := range current.Hooks {
		currentByName[hook.Name] = hook
	}
	usedHookSources := make(map[string]struct{}, len(items))
	for i, item := range items {
		if item.Kind != interaction.ValueObject {
			return operation.Result{}, fmt.Errorf("hooks[%d] must be an object: %w", i, ErrInvalidConfig)
		}
		entries := map[string]interaction.Value{}
		for _, entry := range item.Entries {
			entries[entry.Name] = entry.Value
		}
		source, ok := stringEntry(entries, "source")
		if !ok {
			return operation.Result{}, fmt.Errorf("hooks[%d].source is required: %w", i, ErrInvalidConfig)
		}
		var previous Hook
		if source != "" {
			var exists bool
			previous, exists = currentByName[source]
			if !exists {
				return operation.Result{}, fmt.Errorf("hooks[%d].source %q is unknown: %w", i, source, ErrInvalidConfig)
			}
			if _, duplicate := usedHookSources[source]; duplicate {
				return operation.Result{}, fmt.Errorf("hooks[%d].source %q is duplicated: %w", i, source, ErrInvalidConfig)
			}
			usedHookSources[source] = struct{}{}
		}
		name, ok := stringEntry(entries, "name")
		if !ok || name == "" {
			return operation.Result{}, fmt.Errorf("hooks[%d].name is required: %w", i, ErrInvalidConfig)
		}
		target, ok := stringEntry(entries, "target")
		if !ok || target == "" {
			return operation.Result{}, fmt.Errorf("hooks[%d].target is required: %w", i, ErrInvalidConfig)
		}
		executable, ok := stringEntry(entries, "executable")
		if !ok || executable == "" {
			return operation.Result{}, fmt.Errorf("hooks[%d].executable is required: %w", i, ErrInvalidConfig)
		}
		hook := cloneHook(previous)
		hook.Name = name
		hook.Target = target
		hook.Executable = executable
		if args, present := listEntry(entries, "args"); present {
			hook.Args = make([]string, 0, len(args))
			for _, value := range args {
				if value.Kind != interaction.ValueString {
					return operation.Result{}, fmt.Errorf("hooks[%d].args must be strings: %w", i, ErrInvalidConfig)
				}
				hook.Args = append(hook.Args, value.String)
			}
		}
		if v, present := integerEntry(entries, "timeout_seconds"); present {
			hook.TimeoutSeconds = int(v)
		}
		if v, present := integerEntry(entries, "max_output_bytes"); present {
			hook.MaxOutputBytes = int(v)
		}
		if environment, present := listEntry(entries, "environment"); present {
			hook.Environment, err = mergeEnvironment(previous.Environment, environment, i)
			if err != nil {
				return operation.Result{}, err
			}
		}
		// normalizeHook enforces the same rules as construction, including the
		// executable existence check.
		if _, err := normalizeHook(hook); err != nil {
			return operation.Result{}, fmt.Errorf("hooks[%d]: %w", i, err)
		}
		hooks = append(hooks, hook)
	}
	updated := Config{Hooks: hooks}
	normalized, err := normalizeHooks(updated)
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
	output, err := json.Marshal(map[string]any{"hooks": len(hooks), "restart_required": !reflect.DeepEqual(normalized, o.active)})
	if err != nil {
		return operation.Result{}, err
	}
	return operation.Result{Output: output}, nil
}

func scriptHooksValue(hooks []Hook) interaction.Value {
	items := make([]interaction.Value, 0, len(hooks))
	for _, hook := range hooks {
		args := make([]interaction.Value, 0, len(hook.Args))
		for _, arg := range hook.Args {
			args = append(args, interaction.StringValue(arg))
		}
		entries := []interaction.Entry{
			{Name: "source", Value: interaction.StringValue(hook.Name)},
			{Name: "name", Value: interaction.StringValue(hook.Name)},
			{Name: "target", Value: interaction.StringValue(hook.Target)},
			{Name: "executable", Value: interaction.StringValue(hook.Executable)},
			{Name: "args", Value: interaction.ListValue(args)},
			{Name: "environment", Value: environmentNamesValue(hook.Environment)},
		}
		if hook.TimeoutSeconds != 0 {
			entries = append(entries, interaction.Entry{Name: "timeout_seconds", Value: interaction.IntegerValue(int64(hook.TimeoutSeconds))})
		}
		if hook.MaxOutputBytes != 0 {
			entries = append(entries, interaction.Entry{Name: "max_output_bytes", Value: interaction.IntegerValue(int64(hook.MaxOutputBytes))})
		}
		items = append(items, interaction.ObjectValue(entries))
	}
	return interaction.ListValue(items)
}

func environmentNamesValue(environment map[string]string) interaction.Value {
	names := make([]string, 0, len(environment))
	for name := range environment {
		names = append(names, name)
	}
	sort.Strings(names)
	items := make([]interaction.Value, 0, len(names))
	for _, name := range names {
		items = append(items, interaction.ObjectValue([]interaction.Entry{
			{Name: "name", Value: interaction.StringValue(name)},
			{Name: "source", Value: interaction.StringValue(name)},
			{Name: "action", Value: interaction.StringValue(environmentValueKeep)},
		}))
	}
	return interaction.ListValue(items)
}

func mergeEnvironment(current map[string]string, items []interaction.Value, hookIndex int) (map[string]string, error) {
	updated := make(map[string]string, len(items))
	usedSources := make(map[string]struct{}, len(items))
	for i, item := range items {
		if item.Kind != interaction.ValueObject {
			return nil, fmt.Errorf("hooks[%d].environment[%d] must be an object: %w", hookIndex, i, ErrInvalidConfig)
		}
		entries := make(map[string]interaction.Value, len(item.Entries))
		for _, entry := range item.Entries {
			entries[entry.Name] = entry.Value
		}
		name, ok := stringEntry(entries, "name")
		if !ok || name == "" {
			return nil, fmt.Errorf("hooks[%d].environment[%d].name is required: %w", hookIndex, i, ErrInvalidConfig)
		}
		if _, duplicate := updated[name]; duplicate {
			return nil, fmt.Errorf("hooks[%d].environment contains duplicate name %q: %w", hookIndex, name, ErrInvalidConfig)
		}
		source, ok := stringEntry(entries, "source")
		if !ok {
			return nil, fmt.Errorf("hooks[%d].environment[%d].source is required: %w", hookIndex, i, ErrInvalidConfig)
		}
		var previous string
		if source != "" {
			var exists bool
			previous, exists = current[source]
			if !exists {
				return nil, fmt.Errorf("hooks[%d].environment[%d].source %q is unknown: %w", hookIndex, i, source, ErrInvalidConfig)
			}
			if _, duplicate := usedSources[source]; duplicate {
				return nil, fmt.Errorf("hooks[%d].environment[%d].source %q is duplicated: %w", hookIndex, i, source, ErrInvalidConfig)
			}
			usedSources[source] = struct{}{}
		}
		action, ok := stringEntry(entries, "action")
		if !ok {
			return nil, fmt.Errorf("hooks[%d].environment[%d].action is required: %w", hookIndex, i, ErrInvalidConfig)
		}
		switch action {
		case environmentValueKeep:
			if source == "" {
				return nil, fmt.Errorf("hooks[%d].environment[%d] cannot keep a value without an existing source: %w", hookIndex, i, ErrInvalidConfig)
			}
			updated[name] = previous
		case environmentValueReplace:
			value, supplied := entries["value"]
			if !supplied || value.Kind != interaction.ValueString {
				return nil, fmt.Errorf("hooks[%d].environment[%d].value is required when replacing: %w", hookIndex, i, ErrInvalidConfig)
			}
			updated[name] = value.String
		case environmentValueClear:
			updated[name] = ""
		default:
			return nil, fmt.Errorf("hooks[%d].environment[%d].action %q is unsupported: %w", hookIndex, i, action, ErrInvalidConfig)
		}
	}
	return updated, nil
}

func hookSourceOptions(hooks []Hook) []interaction.Option {
	options := []interaction.Option{{Value: "", Label: "New hook"}}
	for _, hook := range hooks {
		options = append(options, interaction.Option{Value: hook.Name, Label: hook.Name})
	}
	return options
}

func environmentSourceOptions(hooks []Hook) []interaction.Option {
	names := make(map[string]struct{})
	for _, hook := range hooks {
		for name := range hook.Environment {
			names[name] = struct{}{}
		}
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	options := []interaction.Option{{Value: "", Label: "New entry"}}
	for _, name := range ordered {
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

func cloneHook(hook Hook) Hook {
	hook.Args = append([]string(nil), hook.Args...)
	hook.Environment = cloneEnvironment(hook.Environment)
	return hook
}

func cloneEnvironment(environment map[string]string) map[string]string {
	if environment == nil {
		return nil
	}
	cloned := make(map[string]string, len(environment))
	for name, value := range environment {
		cloned[name] = value
	}
	return cloned
}

// validateHooks checks duplicate names, mirroring construction.
func validateHooks(cfg Config) error {
	_, err := normalizeHooks(cfg)
	return err
}

func normalizeHooks(cfg Config) ([]normalizedHook, error) {
	seen := make(map[string]struct{}, len(cfg.Hooks))
	normalized := make([]normalizedHook, 0, len(cfg.Hooks))
	for i, candidate := range cfg.Hooks {
		hook, err := normalizeHook(candidate)
		if err != nil {
			return nil, fmt.Errorf("hooks[%d]: %w", i, err)
		}
		if _, exists := seen[hook.name]; exists {
			return nil, fmt.Errorf("hooks[%d] duplicate name %q: %w", i, hook.name, ErrInvalidConfig)
		}
		seen[hook.name] = struct{}{}
		normalized = append(normalized, hook)
	}
	return normalized, nil
}

func stringEntry(entries map[string]interaction.Value, name string) (string, bool) {
	value, ok := entries[name]
	if !ok || value.Kind != interaction.ValueString {
		return "", false
	}
	return value.String, true
}

func integerEntry(entries map[string]interaction.Value, name string) (int64, bool) {
	value, ok := entries[name]
	if !ok || value.Kind != interaction.ValueInteger {
		return 0, false
	}
	return value.Integer, true
}

func listEntry(entries map[string]interaction.Value, name string) ([]interaction.Value, bool) {
	value, ok := entries[name]
	if !ok || value.Kind != interaction.ValueList {
		return nil, false
	}
	return value.Items, true
}

func answerList(response interaction.Response, name string) ([]interaction.Value, bool) {
	for _, answer := range response.Values {
		if answer.Name == name && answer.Value.Kind == interaction.ValueList {
			return answer.Value.Items, true
		}
	}
	return nil, false
}
