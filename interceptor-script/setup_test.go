package script

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
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

func TestSetupPreservesHookEnvironmentWithoutProjectingValues(t *testing.T) {
	hook := helperHook(t, "reject")
	hook.Environment["API_TOKEN"] = "secret"
	scope := testStateScope{dir: writeTestConfig(t, Config{Hooks: []Hook{hook}})}
	exports, _, err := New(context.Background(), Dependencies{State: scope})
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
	if !reflect.DeepEqual(stored.Hooks[0].Environment, hook.Environment) {
		t.Fatalf("environment = %#v, want %#v", stored.Hooks[0].Environment, hook.Environment)
	}
	var output struct {
		Hooks           int  `json:"hooks"`
		RestartRequired bool `json:"restart_required"`
	}
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	if output.Hooks != 1 || output.RestartRequired {
		t.Fatalf("output = %#v", output)
	}

	hooksField := findSetupField(t, channel.request.Fields, "hooks")
	environmentField := findSetupField(t, hooksField.Element.Fields, "environment")
	valueField := findSetupField(t, environmentField.Element.Fields, "value")
	if !valueField.Sensitive || valueField.Default != nil {
		t.Fatalf("environment value field = %#v", valueField)
	}
	defaultHook := hooksField.Default.Items[0]
	defaultEnvironment := findEntry(t, defaultHook.Entries, "environment").Value
	for _, item := range defaultEnvironment.Items {
		for _, entry := range item.Entries {
			if entry.Name == "value" || entry.Value.String == "secret" {
				t.Fatalf("environment default exposes a value: %#v", item)
			}
		}
	}
}

func TestSetupRejectsStaleConfiguration(t *testing.T) {
	hook := helperHook(t, "reject")
	scope := testStateScope{dir: writeTestConfig(t, Config{Hooks: []Hook{hook}})}
	exports, _, err := New(context.Background(), Dependencies{State: scope})
	if err != nil {
		t.Fatal(err)
	}
	channel := &setupChannel{respond: func(request interaction.Request) (interaction.Response, error) {
		changed := hook
		changed.Name = "concurrent"
		if err := saveConfig(scope.Dir(), Config{Hooks: []Hook{changed}}); err != nil {
			return interaction.Response{}, err
		}
		return defaultsResponse(request)
	}}
	_, err = exports.Operations[0].Invoke(context.Background(), operation.Request{Interaction: channel})
	if !errors.Is(err, ErrConfigConflict) {
		t.Fatalf("error = %v, want ErrConfigConflict", err)
	}
}

func TestSetupRenamesHookAndAppliesExplicitEnvironmentActions(t *testing.T) {
	hook := helperHook(t, "reject")
	hook.Environment = map[string]string{
		"KEEP": "secret", "REPLACE": "old", "CLEAR": "old", "DELETE": "old",
	}
	scope := testStateScope{dir: writeTestConfig(t, Config{Hooks: []Hook{hook}})}
	exports, _, err := New(context.Background(), Dependencies{State: scope})
	if err != nil {
		t.Fatal(err)
	}
	replacement := "new"
	empty := ""
	channel := &setupChannel{respond: func(request interaction.Request) (interaction.Response, error) {
		hooks := findSetupField(t, request.Fields, "hooks")
		source := findSetupField(t, hooks.Element.Fields, "source")
		environment := findSetupField(t, hooks.Element.Fields, "environment")
		environmentSource := findSetupField(t, environment.Element.Fields, "source")
		action := findSetupField(t, environment.Element.Fields, "action")
		if source.Kind != interaction.FieldChoice || len(source.Options) != 2 || environmentSource.Kind != interaction.FieldString || len(environmentSource.Options) != 5 || action.Kind != interaction.FieldChoice || len(action.Options) != 3 {
			t.Fatalf("source/action fields = %#v / %#v / %#v", source, environmentSource, action)
		}
		return interaction.Response{Values: []interaction.Answer{{Name: "hooks", Value: interaction.ListValue([]interaction.Value{
			interaction.ObjectValue([]interaction.Entry{
				{Name: "source", Value: interaction.StringValue(hook.Name)},
				{Name: "name", Value: interaction.StringValue("renamed")},
				{Name: "target", Value: interaction.StringValue(hook.Target)},
				{Name: "executable", Value: interaction.StringValue(hook.Executable)},
				{Name: "environment", Value: interaction.ListValue([]interaction.Value{
					setupEnvironmentValue("KEEP", "RENAMED", environmentValueKeep, nil),
					setupEnvironmentValue("REPLACE", "REPLACE", environmentValueReplace, &replacement),
					setupEnvironmentValue("CLEAR", "CLEAR", environmentValueClear, nil),
					setupEnvironmentValue("", "EMPTY", environmentValueReplace, &empty),
				})},
			}),
		})}}}, nil
	}}
	if _, err := exports.Operations[0].Invoke(context.Background(), operation.Request{Interaction: channel}); err != nil {
		t.Fatal(err)
	}
	stored, err := loadConfig(scope.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Hooks) != 1 || stored.Hooks[0].Name != "renamed" || !reflect.DeepEqual(stored.Hooks[0].Args, hook.Args) || stored.Hooks[0].TimeoutSeconds != hook.TimeoutSeconds {
		t.Fatalf("stored hook = %#v", stored.Hooks)
	}
	want := map[string]string{"RENAMED": "secret", "REPLACE": "new", "CLEAR": "", "EMPTY": ""}
	if !reflect.DeepEqual(stored.Hooks[0].Environment, want) {
		t.Fatalf("environment = %#v, want %#v", stored.Hooks[0].Environment, want)
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
	answers := make([]interaction.Answer, 0, len(request.Fields))
	for _, field := range request.Fields {
		if field.Default != nil {
			answers = append(answers, interaction.Answer{Name: field.Name, Value: *field.Default})
		}
	}
	return interaction.Response{Values: answers}, nil
}

func findSetupField(t *testing.T, fields []interaction.Field, name string) interaction.Field {
	t.Helper()
	for _, field := range fields {
		if field.Name == name {
			return field
		}
	}
	t.Fatalf("field %q not found", name)
	return interaction.Field{}
}

func findEntry(t *testing.T, entries []interaction.Entry, name string) interaction.Entry {
	t.Helper()
	for _, entry := range entries {
		if entry.Name == name {
			return entry
		}
	}
	t.Fatalf("entry %q not found", name)
	return interaction.Entry{}
}
