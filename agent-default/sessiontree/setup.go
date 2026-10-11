package sessiontree

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/ingot-agent/ingot-abi/state"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/operation"
	"github.com/pelletier/go-toml/v2"
)

const setupOperationName = "subagents"

var (
	ErrConfigConflict = errors.New("agent.default subagent configuration changed during interaction")
	configCommitMu    sync.Mutex
)

// Tool discovery is published by ValidateTools, avoiding a dependency cycle
// from session-tree through tool.Runtime and tool-subagent back to Children.
type setupOperation struct {
	scope            state.Scope
	tree             *tree
	supportsChildren bool
	tools            atomic.Pointer[map[string]struct{}]
}

func (*setupOperation) Definition() operation.Definition {
	return operation.Definition{
		Name: setupOperationName, Group: "agent-default",
		Description:  "Review and update child agent types, tool allowlists and dispatch permissions. Saved changes apply to new children immediately.",
		InputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`),
		OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["restart_required"],"properties":{"restart_required":{"type":"boolean"}}}`),
	}
}

func (o *setupOperation) Invoke(ctx context.Context, request operation.Request) (operation.Result, error) {
	if err := ctx.Err(); err != nil {
		return operation.Result{}, err
	}
	if isNil(o.scope) || o.scope.Dir() == "" {
		return operation.Result{}, fmt.Errorf("subagent state scope is required: %w", ErrInvalidConfig)
	}
	tools := o.tools.Load()
	if tools == nil {
		return operation.Result{}, fmt.Errorf("subagent tools have not been discovered: %w", operation.ErrUnavailable)
	}
	if isNil(request.Interaction) {
		return operation.Result{}, interaction.ErrUnavailable
	}
	current, err := readConfiguration(o.scope.Dir())
	if err != nil {
		return operation.Result{}, err
	}
	mode := "builtin"
	var document fileConfig
	if current != nil {
		document, err = decodeConfiguration(current)
		if err != nil {
			return operation.Result{}, err
		}
		if _, err := configurationFromDocument(document); err != nil {
			return operation.Result{}, fmt.Errorf("%w: %w", err, ErrInvalidConfig)
		}
		mode = "custom"
		if len(document.Agents) == 0 {
			mode = "disabled"
		}
	} else {
		defaults, err := o.resolve(configuration{builtin: true}, *tools)
		if err != nil {
			return operation.Result{}, err
		}
		document = configurationDocument(defaults)
	}
	response, err := request.Interaction.Request(ctx, interaction.Request{
		Name:        setupOperationName,
		Description: "Choose child agent configuration. Saved changes apply to new children immediately; existing children keep their accepted definitions.",
		Fields: []interaction.Field{{
			Name: "mode", Label: "Child agents", Kind: interaction.FieldChoice, Required: true,
			Default: valuePointer(interaction.StringValue(mode)),
			Options: []interaction.Option{
				{Value: "builtin", Label: "Built-in types", Description: "Use coder, explorer and reviewer when child-agent tools are installed."},
				{Value: "custom", Label: "Custom types", Description: "Replace the complete list of child agent definitions and permissions."},
				{Value: "disabled", Label: "Disabled", Description: "Disable child agent types."},
			},
		}},
	})
	if err != nil {
		return operation.Result{}, err
	}
	selected, err := answerValue(response, "mode", interaction.ValueString)
	if err != nil {
		return operation.Result{}, err
	}
	var candidate configuration
	var saved []byte
	switch selected.String {
	case "builtin":
		candidate = configuration{builtin: true}
	case "disabled":
		document = fileConfig{Version: configVersion}
	case "custom":
		if !o.supportsChildren {
			return operation.Result{}, agent.ErrChildUnsupported
		}
		if _, ok := (*tools)[submitToolName]; !ok {
			return operation.Result{}, fmt.Errorf("custom child agents require %s: %w", submitToolName, ErrInvalidConfig)
		}
		response, err := request.Interaction.Request(ctx, configurationRequest(document, *tools))
		if err != nil {
			return operation.Result{}, err
		}
		document, err = configurationAnswers(response)
		if err != nil {
			return operation.Result{}, err
		}
	default:
		return operation.Result{}, fmt.Errorf("invalid subagent mode: %w", ErrInvalidConfig)
	}
	if !candidate.builtin {
		candidate, err = configurationFromDocument(document)
		if err != nil {
			return operation.Result{}, fmt.Errorf("%w: %w", err, ErrInvalidConfig)
		}
		saved, err = toml.Marshal(document)
		if err != nil {
			return operation.Result{}, err
		}
	}
	candidate, err = o.resolve(candidate, *tools)
	if err != nil {
		return operation.Result{}, err
	}
	configCommitMu.Lock()
	defer configCommitMu.Unlock()
	if err := ctx.Err(); err != nil {
		return operation.Result{}, err
	}
	latest, err := readConfiguration(o.scope.Dir())
	if err != nil {
		return operation.Result{}, err
	}
	if (latest == nil) != (current == nil) || !bytes.Equal(latest, current) {
		return operation.Result{}, ErrConfigConflict
	}
	if err := ctx.Err(); err != nil {
		return operation.Result{}, err
	}
	if err := saveConfiguration(o.scope.Dir(), saved); err != nil {
		return operation.Result{}, err
	}
	// Publication cannot fail or be canceled after the persistence commit.
	o.tree.config.Store(&candidate)
	return operation.Result{Output: json.RawMessage(`{"restart_required":false}`)}, nil
}

func (o *setupOperation) resolve(config configuration, available map[string]struct{}) (configuration, error) {
	if config.builtin {
		var err error
		if _, installed := available[submitToolName]; installed {
			config, err = builtinConfiguration(available)
		} else {
			config, err = configurationFromDocument(fileConfig{Version: configVersion})
		}
		if err != nil {
			return configuration{}, err
		}
	}
	if config.enabled && !o.supportsChildren {
		return configuration{}, fmt.Errorf("enabled child agents require child Session storage and workspace management: %w", agent.ErrChildUnsupported)
	}
	if err := validateConfigurationTools(config, available); err != nil {
		return configuration{}, err
	}
	return config, nil
}

func configurationDocument(config configuration) fileConfig {
	document := fileConfig{Version: configVersion, RootAllowedTypes: config.rootAllowed}
	names := make([]string, 0, len(config.definitions))
	for name := range config.definitions {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		entry := config.definitions[name]
		document.Agents = append(document.Agents, agentConfig{
			Name: name, Description: entry.info.Description, SystemPrompt: entry.definition.SystemPrompt,
			Tools: entry.definition.Tools, AllowedChildTypes: entry.definition.AllowedChildTypes,
		})
	}
	return document
}

func configurationRequest(document fileConfig, available map[string]struct{}) interaction.Request {
	toolNames := make([]string, 0, len(available))
	for name := range available {
		toolNames = append(toolNames, name)
	}
	sort.Strings(toolNames)
	toolOptions := make([]interaction.Option, 0, len(toolNames))
	for _, name := range toolNames {
		toolOptions = append(toolOptions, interaction.Option{Value: name, Label: name})
	}
	typeOptions := make([]interaction.Option, 0, len(document.Agents))
	agents := make([]interaction.Value, 0, len(document.Agents))
	for _, entry := range document.Agents {
		typeOptions = append(typeOptions, interaction.Option{Value: entry.Name, Label: entry.Name})
		agents = append(agents, interaction.ObjectValue([]interaction.Entry{
			{Name: "name", Value: interaction.StringValue(entry.Name)},
			{Name: "description", Value: interaction.StringValue(entry.Description)},
			{Name: "system_prompt", Value: interaction.StringValue(entry.SystemPrompt)},
			{Name: "tools", Value: interaction.StringsValue(entry.Tools)},
			{Name: "allowed_child_types", Value: namesValue(entry.AllowedChildTypes)},
		}))
	}
	// Types may be added or renamed in this same replacement list. Suggestions
	// cannot be a closed set until those definitions have been submitted.
	typeList := func(name, label string, values []string) interaction.Field {
		return interaction.Field{
			Name: name, Label: label, Kind: interaction.FieldList, Required: true,
			Description: "Names declared in the submitted agent types. An empty list permits no children.",
			Default:     valuePointer(namesValue(values)),
			Element:     &interaction.Field{Name: "type", Kind: interaction.FieldString, Required: true, Options: typeOptions},
		}
	}
	return interaction.Request{
		Name:        setupOperationName + ".types",
		Description: "Replace all child agent definitions and permissions for new children. Update references when renaming or removing a type. Existing children keep their accepted definitions.",
		Fields: []interaction.Field{
			typeList("root_allowed_types", "Root allowed types", document.RootAllowedTypes),
			{Name: "agents", Label: "Agent types", Kind: interaction.FieldList, Required: true,
				Default: valuePointer(interaction.ListValue(agents)),
				Element: &interaction.Field{Name: "agent", Kind: interaction.FieldObject, Fields: []interaction.Field{
					{Name: "name", Label: "Name", Kind: interaction.FieldString, Required: true},
					{Name: "description", Label: "Description", Kind: interaction.FieldString, Required: true},
					{Name: "system_prompt", Label: "System prompt", Kind: interaction.FieldString, Required: true},
					{Name: "tools", Label: "Allowed tools", Kind: interaction.FieldMultiChoice, Required: true, Options: toolOptions,
						Description: "Must include submit_agent_result.", Default: valuePointer(interaction.StringsValue([]string{submitToolName}))},
					typeList("allowed_child_types", "Allowed child types", nil),
				}},
			},
		},
	}
}

func configurationAnswers(response interaction.Response) (fileConfig, error) {
	document := fileConfig{Version: configVersion}
	roots, err := answerValue(response, "root_allowed_types", interaction.ValueList)
	if err != nil {
		return document, err
	}
	document.RootAllowedTypes, err = nameAnswers(roots)
	if err != nil {
		return document, err
	}
	agents, err := answerValue(response, "agents", interaction.ValueList)
	if err != nil {
		return document, err
	}
	for _, item := range agents.Items {
		if item.Kind != interaction.ValueObject {
			return document, fmt.Errorf("agent must be an object: %w", ErrInvalidConfig)
		}
		fields := interaction.Response{}
		for _, entry := range item.Entries {
			fields.Values = append(fields.Values, interaction.Answer{Name: entry.Name, Value: entry.Value})
		}
		var entry agentConfig
		for _, field := range []struct {
			name  string
			value *string
		}{{"name", &entry.Name}, {"description", &entry.Description}, {"system_prompt", &entry.SystemPrompt}} {
			value, err := answerValue(fields, field.name, interaction.ValueString)
			if err != nil {
				return document, err
			}
			*field.value = value.String
		}
		tools, err := answerValue(fields, "tools", interaction.ValueStrings)
		if err != nil {
			return document, err
		}
		entry.Tools = tools.Strings
		children, err := answerValue(fields, "allowed_child_types", interaction.ValueList)
		if err != nil {
			return document, err
		}
		entry.AllowedChildTypes, err = nameAnswers(children)
		if err != nil {
			return document, err
		}
		document.Agents = append(document.Agents, entry)
	}
	return document, nil
}

func answerValue(response interaction.Response, name string, kind interaction.ValueKind) (interaction.Value, error) {
	var value interaction.Value
	for _, answer := range response.Values {
		if answer.Name == name {
			if value.Kind != 0 || answer.Value.Kind != kind {
				return value, fmt.Errorf("invalid %s: %w", name, ErrInvalidConfig)
			}
			value = answer.Value
		}
	}
	if value.Kind == 0 {
		return value, fmt.Errorf("%s is required: %w", name, ErrInvalidConfig)
	}
	return value, nil
}

func nameAnswers(value interaction.Value) ([]string, error) {
	names := make([]string, 0, len(value.Items))
	for _, item := range value.Items {
		if item.Kind != interaction.ValueString {
			return nil, fmt.Errorf("type name must be a string: %w", ErrInvalidConfig)
		}
		names = append(names, item.String)
	}
	return names, nil
}

func namesValue(names []string) interaction.Value {
	items := make([]interaction.Value, 0, len(names))
	for _, name := range names {
		items = append(items, interaction.StringValue(name))
	}
	return interaction.ListValue(items)
}

func valuePointer(value interaction.Value) *interaction.Value { return &value }

// A nil payload restores built-ins by removing the explicit override. Otherwise
// rename is the sole commit point; failed writes leave the original untouched.
func saveConfiguration(root string, raw []byte) error {
	path := filepath.Join(root, configFileName)
	if raw == nil {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(root, ".subagents-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	defer func() { _ = file.Close() }()
	if _, err := file.Write(raw); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

var _ operation.Operation = (*setupOperation)(nil)
