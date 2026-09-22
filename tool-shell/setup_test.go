package toolshell

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/operation"
)

type setupChannel struct {
	request interaction.Request
	respond func(interaction.Request) (interaction.Response, error)
}

func (c *setupChannel) Request(_ context.Context, request interaction.Request) (interaction.Response, error) {
	c.request = request
	return c.respond(request)
}

func (*setupChannel) Emit(context.Context, interaction.Event) error { return nil }
func (*setupChannel) Set(context.Context, interaction.State) error  { return nil }
func (*setupChannel) Clear(context.Context, string) error           { return nil }

func TestSetupPreservesEnvironmentSecretsAndDefaultInheritance(t *testing.T) {
	scope := testStateScope{dir: writeTestConfig(t, Config{
		Environment: map[string]string{"API_TOKEN": "secret"},
		InheritEnv:  nil,
	})}
	exports, _, err := New(context.Background(), Dependencies{
		Workspace: staticResolver{},
		State:     scope,
	})
	if err != nil {
		t.Fatal(err)
	}
	channel := &setupChannel{respond: defaultsResponse}
	result, err := exports.Operations[0].Invoke(context.Background(), operation.Request{Interaction: channel})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := loadConfig(scope.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if stored.Environment["API_TOKEN"] != "secret" {
		t.Fatalf("environment = %#v", stored.Environment)
	}
	if stored.InheritEnv != nil {
		t.Fatalf("inherit_env = %#v, want nil", stored.InheritEnv)
	}
	var output struct {
		RestartRequired bool `json:"restart_required"`
	}
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	if output.RestartRequired {
		t.Fatal("unchanged effective configuration requires restart")
	}

	environment := findSetupField(t, channel.request, "environment")
	if environment.Default == nil || len(environment.Default.Items) != 1 {
		t.Fatalf("environment default = %#v", environment.Default)
	}
	entries := environment.Default.Items[0].Entries
	for _, entry := range entries {
		if entry.Name == "value" || entry.Value.String == "secret" {
			t.Fatalf("environment default exposes a value: %#v", entries)
		}
	}
	if valueField := findNestedField(t, *environment.Element, "value"); !valueField.Sensitive || valueField.Default != nil {
		t.Fatalf("environment value field = %#v", valueField)
	}
}

func TestSetupRejectsStaleConfiguration(t *testing.T) {
	scope := testStateScope{dir: writeTestConfig(t, Config{TimeoutSeconds: 10})}
	exports, _, err := New(context.Background(), Dependencies{Workspace: staticResolver{}, State: scope})
	if err != nil {
		t.Fatal(err)
	}
	channel := &setupChannel{respond: func(request interaction.Request) (interaction.Response, error) {
		if err := saveConfig(scope.Dir(), Config{TimeoutSeconds: 20}); err != nil {
			return interaction.Response{}, err
		}
		return defaultsResponse(request)
	}}
	_, err = exports.Operations[0].Invoke(context.Background(), operation.Request{Interaction: channel})
	if !errors.Is(err, ErrConfigConflict) {
		t.Fatalf("error = %v, want ErrConfigConflict", err)
	}
	stored, err := loadConfig(scope.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if stored.TimeoutSeconds != 20 {
		t.Fatalf("timeout_seconds = %d, want concurrent value 20", stored.TimeoutSeconds)
	}
}

func TestSetupEnvironmentActionsPreserveRenameReplaceClearAndDelete(t *testing.T) {
	current := Config{Environment: map[string]string{
		"KEEP": "secret", "REPLACE": "old", "CLEAR": "old", "DELETE": "old",
	}}
	scope := testStateScope{dir: writeTestConfig(t, current)}
	exports, _, err := New(context.Background(), Dependencies{Workspace: staticResolver{}, State: scope})
	if err != nil {
		t.Fatal(err)
	}
	replacement := "new"
	empty := ""
	channel := &setupChannel{respond: func(request interaction.Request) (interaction.Response, error) {
		environment := findSetupField(t, request, "environment")
		source := findNestedField(t, *environment.Element, "source")
		action := findNestedField(t, *environment.Element, "action")
		if source.Kind != interaction.FieldChoice || len(source.Options) != 5 || action.Kind != interaction.FieldChoice || len(action.Options) != 3 {
			t.Fatalf("environment source/action fields = %#v / %#v", source, action)
		}
		return interaction.Response{Values: []interaction.Answer{
			{Name: "environment", Value: interaction.ListValue([]interaction.Value{
				setupEnvironmentValue("KEEP", "RENAMED", environmentValueKeep, nil),
				setupEnvironmentValue("REPLACE", "REPLACE", environmentValueReplace, &replacement),
				setupEnvironmentValue("CLEAR", "CLEAR", environmentValueClear, nil),
				setupEnvironmentValue("", "EMPTY", environmentValueReplace, &empty),
			})},
			{Name: "inherit_mode", Value: interaction.StringValue(inheritModeAll)},
		}}, nil
	}}
	if _, err := exports.Operations[0].Invoke(context.Background(), operation.Request{Interaction: channel}); err != nil {
		t.Fatal(err)
	}
	stored, err := loadConfig(scope.Dir())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"RENAMED": "secret", "REPLACE": "new", "CLEAR": "", "EMPTY": ""}
	if !reflect.DeepEqual(stored.Environment, want) {
		t.Fatalf("environment = %#v, want %#v", stored.Environment, want)
	}
	running := exports.Tools[0].(*shellTool).config.Load()
	if !slices.Contains(running.environment, "CLEAR=") || !slices.Contains(running.environment, "EMPTY=") ||
		!slices.Contains(running.environment, "RENAMED=secret") || !slices.Contains(running.environment, "REPLACE=new") || !running.inheritEnvironment {
		t.Fatalf("running config = %#v", running)
	}
}

func setupEnvironmentValue(source, name, action string, value *string) interaction.Value {
	entries := []interaction.Entry{
		{Name: "source", Value: interaction.StringValue(source)},
		{Name: "name", Value: interaction.StringValue(name)},
		{Name: "action", Value: interaction.StringValue(action)},
	}
	if value != nil {
		entries = append(entries, interaction.Entry{Name: "value", Value: interaction.StringValue(*value)})
	}
	return interaction.ObjectValue(entries)
}

func defaultsResponse(request interaction.Request) (interaction.Response, error) {
	values := make([]interaction.Answer, 0, len(request.Fields))
	for _, field := range request.Fields {
		if field.Default != nil {
			values = append(values, interaction.Answer{Name: field.Name, Value: *field.Default})
		}
	}
	return interaction.Response{Values: values}, nil
}

func findSetupField(t *testing.T, request interaction.Request, name string) interaction.Field {
	t.Helper()
	for _, field := range request.Fields {
		if field.Name == name {
			return field
		}
	}
	t.Fatalf("field %q not found", name)
	return interaction.Field{}
}

func findNestedField(t *testing.T, object interaction.Field, name string) interaction.Field {
	t.Helper()
	for _, field := range object.Fields {
		if field.Name == name {
			return field
		}
	}
	t.Fatalf("nested field %q not found", name)
	return interaction.Field{}
}
