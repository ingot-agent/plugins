package contextcompact

import (
	"context"
	"errors"
	"testing"

	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/logging"
	"github.com/ingot-agent/sdk/operation"
)

// answerInteraction returns a fixed interaction response.
type answerInteraction struct {
	values []interaction.Answer
	err    error
	fields []interaction.Field
}

func (*answerInteraction) Emit(context.Context, interaction.Event) error { return nil }

func (*answerInteraction) Set(context.Context, interaction.State) error { return nil }

func (*answerInteraction) Clear(context.Context, string) error { return nil }

func (a *answerInteraction) Request(_ context.Context, request interaction.Request) (interaction.Response, error) {
	a.fields = request.Fields
	if a.err != nil {
		return interaction.Response{}, a.err
	}
	return interaction.Response{Values: a.values}, nil
}

func newSetupTest(t *testing.T, cfg Config) (*setupOperation, *compactor, *answerInteraction) {
	t.Helper()
	dir := writeTestConfig(t, cfg)
	loader, err := newConfigLoader(dir, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	instance := &compactor{loader: loader, logger: discardLogger()}
	return newSetupOperation(testStateScope{dir: dir}, nil, instance, loader), instance, &answerInteraction{}
}

func discardLogger() logging.Logger { return logging.Discard() }

func TestSetupExposesOnlySixFields(t *testing.T) {
	op, _, answer := newSetupTest(t, Config{})
	if _, err := op.Invoke(context.Background(), operation.Request{Interaction: answer}); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(answer.fields))
	for _, field := range answer.fields {
		names = append(names, field.Name)
	}
	want := []string{"provider", "model", "trigger_tokens", "target_tokens", "protect_recent_tokens", "summary_max_tokens"}
	if len(names) != len(want) {
		t.Fatalf("fields = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("fields = %v, want %v", names, want)
		}
	}
}

func TestSetupPersistsAndAppliesImmediately(t *testing.T) {
	op, instance, answer := newSetupTest(t, Config{})
	answer.values = []interaction.Answer{
		{Name: "trigger_tokens", Value: interaction.IntegerValue(1234)},
		{Name: "target_tokens", Value: interaction.IntegerValue(600)},
		{Name: "protect_recent_tokens", Value: interaction.IntegerValue(300)},
		{Name: "summary_max_tokens", Value: interaction.IntegerValue(512)},
		{Name: "model", Value: interaction.StringValue("summary-model")},
	}
	if _, err := op.Invoke(context.Background(), operation.Request{Interaction: answer}); err != nil {
		t.Fatal(err)
	}
	active := instance.loader.snapshot()
	if active.triggerTokens != 1234 || active.targetTokens != 600 ||
		active.protectRecentTokens != 300 || active.summaryMaxTokens != 512 {
		t.Fatalf("active config = %#v", active)
	}
	if active.model != "summary-model" {
		t.Fatalf("model = %q", active.model)
	}
	// Restart-equivalent load must agree with the in-memory snapshot.
	persisted, _, err := loadConfig(op.scope.Dir())
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := normalizeConfig(persisted)
	if err != nil {
		t.Fatal(err)
	}
	if normalized != active {
		t.Fatalf("persisted = %#v, active = %#v", normalized, active)
	}
}

func TestSetupRejectsInvalidCombination(t *testing.T) {
	op, _, answer := newSetupTest(t, Config{})
	answer.values = []interaction.Answer{
		{Name: "trigger_tokens", Value: interaction.IntegerValue(1000)},
		{Name: "target_tokens", Value: interaction.IntegerValue(1000)},
	}
	if _, err := op.Invoke(context.Background(), operation.Request{Interaction: answer}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error = %v, want ErrInvalidConfig", err)
	}
}

func TestSetupPropagatesInteractionError(t *testing.T) {
	op, _, answer := newSetupTest(t, Config{})
	answer.err = errors.New("user cancelled")
	if _, err := op.Invoke(context.Background(), operation.Request{Interaction: answer}); err == nil {
		t.Fatal("expected an error")
	}
	// A cancelled interaction must not persist anything.
	persisted, _, err := loadConfig(op.scope.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if persisted.TriggerTokens != 0 {
		t.Fatalf("config changed on failure: %#v", persisted)
	}
}
