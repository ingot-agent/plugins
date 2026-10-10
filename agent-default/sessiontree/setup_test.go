package sessiontree

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"testing"

	ingotabi "github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/operation"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/tool"
	"github.com/ingot-agent/sdk/workspace"
)

type setupChannel struct {
	interaction.Channel
	request func(context.Context, interaction.Request) (interaction.Response, error)
}

func (c setupChannel) Request(ctx context.Context, request interaction.Request) (interaction.Response, error) {
	return c.request(ctx, request)
}

func setupExports(t *testing.T, root string, support bool, names ...string) Exports {
	t.Helper()
	deps := Dependencies{State: stateScope(root)}
	if support {
		deps.Repository = ingotabi.Some[agent.ChildSessionRepository](newMemoryRepository())
		deps.Workspace = ingotabi.Some[workspace.Manager](&memoryWorkspace{assigned: make(map[session.ID]workspace.Binding)})
	}
	exports, cleanup, err := New(context.Background(), deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cleanup(context.Background()) })
	definitions := make([]tool.Definition, 0, len(names))
	for _, name := range names {
		definitions = append(definitions, tool.Definition{Name: name})
	}
	if err := exports.Control.ValidateTools(definitions); err != nil {
		t.Fatal(err)
	}
	if len(exports.Operations) != 1 {
		t.Fatalf("operations = %d, want one independent subagent operation", len(exports.Operations))
	}
	return exports
}

func defaultAnswers(request interaction.Request) interaction.Response {
	response := interaction.Response{}
	for _, field := range request.Fields {
		if field.Default != nil {
			response.Values = append(response.Values, interaction.Answer{Name: field.Name, Value: *field.Default})
		}
	}
	return response
}

func modeAnswers(mode string) interaction.Response {
	return interaction.Response{Values: []interaction.Answer{{Name: "mode", Value: interaction.StringValue(mode)}}}
}

func customAnswers() interaction.Response {
	return interaction.Response{Values: []interaction.Answer{
		{Name: "root_allowed_types", Value: namesValue([]string{"leader"})},
		{Name: "agents", Value: interaction.ListValue([]interaction.Value{
			interaction.ObjectValue([]interaction.Entry{
				{Name: "name", Value: interaction.StringValue("leader")},
				{Name: "description", Value: interaction.StringValue("Coordinate research")},
				{Name: "system_prompt", Value: interaction.StringValue("Delegate a bounded question.\nSubmit a report.")},
				{Name: "tools", Value: interaction.StringsValue([]string{submitToolName, "spawn_agent"})},
				{Name: "allowed_child_types", Value: namesValue([]string{"researcher"})},
			}),
			interaction.ObjectValue([]interaction.Entry{
				{Name: "name", Value: interaction.StringValue("researcher")},
				{Name: "description", Value: interaction.StringValue("Research a question")},
				{Name: "system_prompt", Value: interaction.StringValue("Inspect files and submit findings.")},
				{Name: "tools", Value: interaction.StringsValue([]string{"read_file", submitToolName})},
				{Name: "allowed_child_types", Value: namesValue(nil)},
			}),
		})},
	}}
}

func invokeSetup(t *testing.T, op operation.Operation, mode string, custom *interaction.Response) {
	t.Helper()
	result, err := op.Invoke(context.Background(), operation.Request{Interaction: setupChannel{request: func(_ context.Context, request interaction.Request) (interaction.Response, error) {
		if request.Name == "subagents" {
			return modeAnswers(mode), nil
		}
		if custom != nil {
			return *custom, nil
		}
		return defaultAnswers(request), nil
	}}})
	if err != nil {
		t.Fatal(err)
	}
	var output map[string]bool
	if err := json.Unmarshal(result.Output, &output); err != nil || len(output) != 1 || output["restart_required"] {
		t.Fatalf("output = %s, want restart_required=false; err=%v", result.Output, err)
	}
}

func TestSubagentSetupPersistsAndAppliesCompleteDefinitions(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	exports := setupExports(t, root, true, submitToolName, "read_file", "spawn_agent")
	op := exports.Operations[0]
	definition := op.Definition()
	if definition.Name != "subagents" || definition.Group != "agent-default" || !json.Valid(definition.InputSchema) || !json.Valid(definition.OutputSchema) {
		t.Fatalf("definition = %#v", definition)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	loopConfig := []byte("max_rounds = 19\n")
	if err := os.WriteFile(filepath.Join(root, "config.toml"), loopConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	answers := customAnswers()
	invokeSetup(t, op, "custom", &answers)
	raw, err := readConfiguration(root)
	if err != nil {
		t.Fatal(err)
	}
	document, err := decodeConfiguration(raw)
	if err != nil {
		t.Fatal(err)
	}
	if document.Version != 1 || !slices.Equal(document.RootAllowedTypes, []string{"leader"}) || len(document.Agents) != 2 {
		t.Fatalf("saved config = %#v", document)
	}
	leader, researcher := document.Agents[0], document.Agents[1]
	if leader.Name != "leader" || leader.Description != "Coordinate research" || leader.SystemPrompt != "Delegate a bounded question.\nSubmit a report." || !slices.Equal(leader.Tools, []string{submitToolName, "spawn_agent"}) || !slices.Equal(leader.AllowedChildTypes, []string{"researcher"}) {
		t.Fatalf("leader = %#v", leader)
	}
	if researcher.Name != "researcher" || !slices.Equal(researcher.Tools, []string{"read_file", submitToolName}) || len(researcher.AllowedChildTypes) != 0 {
		t.Fatalf("researcher = %#v", researcher)
	}
	if got, err := os.ReadFile(filepath.Join(root, "config.toml")); err != nil || !slices.Equal(got, loopConfig) {
		t.Fatalf("loop config modified: %s, %v", got, err)
	}
	info, err := os.Stat(filepath.Join(root, configFileName))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("config permissions: %v, %v", info, err)
	}
	if _, exists := exports.Control.(*tree).config.Load().definitions["leader"]; !exists {
		t.Fatal("saved definitions did not become active immediately")
	}
	// Reopening the form uses the saved configuration and remains live.
	invokeSetup(t, op, "custom", nil)
	restarted := setupExports(t, root, true, submitToolName, "read_file", "spawn_agent")
	if _, exists := restarted.Control.(*tree).config.Load().definitions["leader"]; !exists {
		t.Fatal("saved definition not active after reconstruction")
	}
	invokeSetup(t, restarted.Operations[0], "custom", nil)
}

func TestSubagentSetupBuiltinDisableAndRestore(t *testing.T) {
	root := t.TempDir()
	exports := setupExports(t, root, true, submitToolName, "read_file")
	op := exports.Operations[0]
	invokeSetup(t, op, "builtin", nil)
	invokeSetup(t, op, "custom", nil) // Built-in defaults seed the form.
	config, err := loadConfiguration(root)
	if err != nil || config.builtin || len(config.definitions) != 3 {
		t.Fatalf("customized built-ins = %#v, %v", config, err)
	}
	invokeSetup(t, op, "disabled", nil)
	config, err = loadConfiguration(root)
	if err != nil || config.builtin || config.enabled {
		t.Fatalf("disabled config = %#v, %v", config, err)
	}
	invokeSetup(t, op, "disabled", nil)
	invokeSetup(t, op, "builtin", nil)
	if _, err := os.Stat(filepath.Join(root, configFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("built-in mode did not remove override: %v", err)
	}
}

func TestSubagentSetupRejectsInvalidDefinitionsWithoutWriting(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*interaction.Response)
	}{
		{"unknown root", func(r *interaction.Response) { r.Values[0].Value = namesValue([]string{"missing"}) }},
		{"duplicate roots", func(r *interaction.Response) { r.Values[0].Value = namesValue([]string{"leader", "leader"}) }},
		{"invalid name", func(r *interaction.Response) {
			r.Values[1].Value.Items[0].Entries[0].Value = interaction.StringValue("Bad Name")
		}},
		{"duplicate types", func(r *interaction.Response) {
			r.Values[1].Value.Items = append(r.Values[1].Value.Items, r.Values[1].Value.Items[0])
		}},
		{"empty prompt", func(r *interaction.Response) {
			r.Values[1].Value.Items[0].Entries[2].Value = interaction.StringValue("")
		}},
		{"unavailable tool", func(r *interaction.Response) {
			r.Values[1].Value.Items[0].Entries[3].Value = interaction.StringsValue([]string{submitToolName, "missing"})
		}},
		{"missing submission", func(r *interaction.Response) {
			r.Values[1].Value.Items[0].Entries[3].Value = interaction.StringsValue([]string{"read_file"})
		}},
		{"duplicate tools", func(r *interaction.Response) {
			r.Values[1].Value.Items[0].Entries[3].Value = interaction.StringsValue([]string{submitToolName, submitToolName})
		}},
		{"unknown child", func(r *interaction.Response) {
			r.Values[1].Value.Items[0].Entries[4].Value = namesValue([]string{"missing"})
		}},
		{"wrong tools kind", func(r *interaction.Response) {
			r.Values[1].Value.Items[0].Entries[3].Value = interaction.StringValue(submitToolName)
		}},
		{"missing field", func(r *interaction.Response) {
			r.Values[1].Value.Items[0].Entries = r.Values[1].Value.Items[0].Entries[:4]
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			exports := setupExports(t, root, true, submitToolName, "read_file", "spawn_agent")
			answers := customAnswers()
			active := exports.Control.(*tree).config.Load()
			tc.edit(&answers)
			_, err := exports.Operations[0].Invoke(context.Background(), operation.Request{Interaction: setupChannel{request: func(_ context.Context, request interaction.Request) (interaction.Response, error) {
				if request.Name == "subagents" {
					return modeAnswers("custom"), nil
				}
				return answers, nil
			}}})
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("error = %v", err)
			}
			if raw, err := readConfiguration(root); err != nil || raw != nil {
				t.Fatalf("invalid config persisted: %s, %v", raw, err)
			}
			if exports.Control.(*tree).config.Load() != active {
				t.Fatal("invalid config was published")
			}
		})
	}
}

func TestSubagentSetupUnavailableOrCanceledDoesNotWrite(t *testing.T) {
	for _, stage := range []string{"unavailable", "mode", "types", "before commit"} {
		t.Run(stage, func(t *testing.T) {
			root := t.TempDir()
			exports := setupExports(t, root, true, submitToolName)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			active := exports.Control.(*tree).config.Load()
			var channel interaction.Channel = interaction.Unavailable()
			want := interaction.ErrUnavailable
			if stage != "unavailable" {
				want = context.Canceled
				channel = setupChannel{request: func(_ context.Context, request interaction.Request) (interaction.Response, error) {
					if stage == "mode" || request.Name == "subagents.types" {
						cancel()
						if stage != "before commit" {
							return interaction.Response{}, ctx.Err()
						}
					}
					if request.Name == "subagents" {
						return modeAnswers("custom"), nil
					}
					return defaultAnswers(request), nil
				}}
			}
			_, err := exports.Operations[0].Invoke(ctx, operation.Request{Interaction: channel})
			if !errors.Is(err, want) {
				t.Fatalf("error = %v, want %v", err, want)
			}
			if raw, err := readConfiguration(root); err != nil || raw != nil {
				t.Fatalf("canceled config persisted: %s, %v", raw, err)
			}
			if exports.Control.(*tree).config.Load() != active {
				t.Fatal("canceled config was published")
			}
		})
	}
}

func TestSubagentSetupConcurrentUpdatesConflict(t *testing.T) {
	root := t.TempDir()
	exports := setupExports(t, root, true, submitToolName)
	var ready sync.WaitGroup
	ready.Add(2)
	release := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := exports.Operations[0].Invoke(context.Background(), operation.Request{Interaction: setupChannel{request: func(context.Context, interaction.Request) (interaction.Response, error) {
				ready.Done()
				<-release
				return modeAnswers("disabled"), nil
			}}})
			results <- err
		}()
	}
	ready.Wait()
	close(release)
	first, second := <-results, <-results
	if !((first == nil && errors.Is(second, ErrConfigConflict)) || (second == nil && errors.Is(first, ErrConfigConflict))) {
		t.Fatalf("concurrent errors = %v, %v", first, second)
	}
}

func TestSubagentSetupDetectsExternalEditsBeforeRestore(t *testing.T) {
	root := t.TempDir()
	exports := setupExports(t, root, true, submitToolName)
	invokeSetup(t, exports.Operations[0], "disabled", nil)
	newer := []byte("subagents_config_version = 1\n# external edit\n")
	active := exports.Control.(*tree).config.Load()
	_, err := exports.Operations[0].Invoke(context.Background(), operation.Request{Interaction: setupChannel{request: func(context.Context, interaction.Request) (interaction.Response, error) {
		if err := os.WriteFile(filepath.Join(root, configFileName), newer, 0o600); err != nil {
			t.Fatal(err)
		}
		return modeAnswers("builtin"), nil
	}}})
	if !errors.Is(err, ErrConfigConflict) {
		t.Fatalf("error = %v", err)
	}
	if raw, err := readConfiguration(root); err != nil || !slices.Equal(raw, newer) {
		t.Fatalf("external edit lost: %s, %v", raw, err)
	}
	if exports.Control.(*tree).config.Load() != active {
		t.Fatal("conflicting config was published")
	}
}

func TestSubagentSetupRequiresCapabilitiesAndToolDiscovery(t *testing.T) {
	for _, tc := range []struct {
		name    string
		support bool
		tools   []string
		want    error
	}{
		{"missing storage", false, nil, agent.ErrChildUnsupported},
		{"missing submit tool", true, []string{"read_file"}, ErrInvalidConfig},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			exports := setupExports(t, root, tc.support, tc.tools...)
			_, err := exports.Operations[0].Invoke(context.Background(), operation.Request{Interaction: setupChannel{request: func(context.Context, interaction.Request) (interaction.Response, error) {
				return modeAnswers("custom"), nil
			}}})
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			invokeSetup(t, exports.Operations[0], "disabled", nil)
		})
	}
	exports, cleanup, err := New(context.Background(), Dependencies{State: stateScope(t.TempDir())})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup(context.Background())
	if _, err := exports.Operations[0].Invoke(context.Background(), operation.Request{Interaction: interaction.Unavailable()}); !errors.Is(err, operation.ErrUnavailable) {
		t.Fatalf("undiscovered tool error = %v", err)
	}
}

func TestSaveSubagentConfigurationFailureLeavesNoTemporaryFiles(t *testing.T) {
	root := t.TempDir()
	// A directory at the target forces the final rename to fail.
	if err := os.Mkdir(filepath.Join(root, configFileName), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := saveConfiguration(root, []byte("subagents_config_version = 1\n")); err == nil {
		t.Fatal("expected persistence failure")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || entries[0].Name() != configFileName || !entries[0].IsDir() {
		t.Fatalf("state after failed save = %v, %v", entries, err)
	}
}

func TestSubagentSetupRequestsInstalledToolsAndEditableTypeReferences(t *testing.T) {
	exports := setupExports(t, t.TempDir(), true, submitToolName, "read_file")
	_, err := exports.Operations[0].Invoke(context.Background(), operation.Request{Interaction: setupChannel{request: func(_ context.Context, request interaction.Request) (interaction.Response, error) {
		if request.Name == "subagents" {
			return modeAnswers("custom"), nil
		}
		roots, agents := request.Fields[0], request.Fields[1]
		if roots.Element.Kind != interaction.FieldString || len(roots.Element.Options) != 3 || len(agents.Default.Items) != 3 {
			t.Fatalf("built-in type defaults = %#v", request)
		}
		fields := agents.Element.Fields
		tools, children := fields[3], fields[4]
		if tools.Kind != interaction.FieldMultiChoice || !reflect.DeepEqual(tools.Options, []interaction.Option{{Value: "read_file", Label: "read_file"}, {Value: submitToolName, Label: submitToolName}}) || children.Element.Kind != interaction.FieldString {
			t.Fatalf("tool/type fields = %#v", fields)
		}
		return interaction.Response{}, interaction.ErrUnavailable
	}}})
	if !errors.Is(err, interaction.ErrUnavailable) {
		t.Fatal(err)
	}
}
