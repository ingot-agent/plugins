package modelruntime

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/ingot-agent/plugins/app-webui/modelselection"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/operation"
)

func selectionFixture(t *testing.T) (Exports, setupTestScope, *liveSource) {
	t.Helper()
	scope := setupTestScope{dir: t.TempDir()}
	if err := saveConfig(scope.Dir(), Config{DefaultProvider: "first", DefaultModel: "old", DefaultReasoningEffort: model.ReasoningEffortHigh}); err != nil {
		t.Fatal(err)
	}
	first, second := liveEntry("first", "first"), liveEntry("second", "second")
	first.Models = []model.ModelEntry{{Name: "old", ReasoningEfforts: []model.ReasoningEffort{model.ReasoningEffortHigh}}}
	second.Models = []model.ModelEntry{{Name: "new", ReasoningEfforts: []model.ReasoningEffort{model.ReasoningEffortLow}}}
	source := &liveSource{}
	source.set(first, second)
	exports, _, err := newRuntimeForTest(context.Background(), Dependencies{State: scope, ProviderSources: []model.ProviderSource{source}})
	if err != nil {
		t.Fatal(err)
	}
	return exports, scope, source
}

func TestModelSelectionUpdatesRuntimeDefaultsAndPersists(t *testing.T) {
	ctx := context.Background()
	exports, scope, source := selectionFixture(t)
	initial, err := exports.Selection.Snapshot(ctx)
	if err != nil || !initial.Configured || initial.Current != (modelselection.Selection{Provider: "first", Model: "old", ReasoningEffort: "high"}) || len(initial.Providers) != 2 {
		t.Fatalf("initial = %#v, err = %v", initial, err)
	}
	choice := modelselection.Selection{Provider: "second", Model: "new", ReasoningEffort: "low"}
	selected, err := exports.Selection.Update(ctx, choice, initial.Revision)
	if err != nil || !selected.Configured || selected.Current != choice || selected.Revision == initial.Revision {
		t.Fatalf("selected = %#v, err = %v", selected, err)
	}
	// Both invocation APIs and the resolver immediately see the same defaults.
	assertLiveSelection(t, exports, "second", "new", "second")
	resolved, err := exports.Resolver.ResolveRequest(ctx, model.Request{})
	if err != nil || resolved.ReasoningEffort != model.ReasoningEffortLow {
		t.Fatalf("resolved = %#v, err = %v", resolved, err)
	}
	if _, err := exports.Selection.Update(ctx, initial.Current, initial.Revision); !errors.Is(err, modelselection.ErrConflict) {
		t.Fatalf("stale revision = %v", err)
	}
	// Programmatic callers can still override runtime defaults explicitly.
	resolved, err = exports.Resolver.ResolveRequest(ctx, model.Request{Provider: "first", Model: "old", ReasoningEffort: model.ReasoningEffortHigh})
	if err != nil || resolved.Provider != "first" || resolved.Model != "old" {
		t.Fatalf("explicit selection = %#v, err = %v", resolved, err)
	}
	choice.ReasoningEffort = string(model.ReasoningEffortProviderDefault)
	selected, err = exports.Selection.Update(ctx, choice, selected.Revision)
	if err != nil || selected.Current != choice {
		t.Fatalf("provider default = %#v, err = %v", selected, err)
	}
	stored, err := loadConfig(scope.Dir())
	if err != nil || stored != (Config{DefaultProvider: "second", DefaultModel: "new"}) {
		t.Fatalf("stored = %#v, err = %v", stored, err)
	}
	restarted, _, err := newRuntimeForTest(ctx, Dependencies{State: scope, ProviderSources: []model.ProviderSource{source}})
	if err != nil {
		t.Fatal(err)
	}
	afterRestart, err := restarted.Selection.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(afterRestart, selected) {
		t.Fatalf("restart = %#v, err = %v", afterRestart, err)
	}
	resolved, err = restarted.Resolver.ResolveRequest(ctx, model.Request{})
	if err != nil || resolved.ReasoningEffort != "" {
		t.Fatalf("provider default was not cleared: %#v, err = %v", resolved, err)
	}
}

func TestModelSelectionRejectsInvalidAndChangedDirectory(t *testing.T) {
	ctx := context.Background()
	exports, scope, source := selectionFixture(t)
	initial, err := exports.Selection.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, choice := range []modelselection.Selection{
		{Provider: "missing", Model: "new", ReasoningEffort: "low"},
		{Provider: "first", Model: "new", ReasoningEffort: "high"},
		{Provider: "second", Model: "new", ReasoningEffort: "high"},
		{Provider: "second", Model: "new", ReasoningEffort: ""},
	} {
		if _, err := exports.Selection.Update(ctx, choice, initial.Revision); !errors.Is(err, modelselection.ErrInvalid) {
			t.Fatalf("choice %#v: %v", choice, err)
		}
	}
	unchanged, err := exports.Selection.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(initial, unchanged) {
		t.Fatalf("invalid update changed selection: %#v, err = %v", unchanged, err)
	}
	// Re-read the directory at submission, even when defaults did not change.
	source.set(liveEntry("second", "second"))
	choice := modelselection.Selection{Provider: "second", Model: "new", ReasoningEffort: "providerDefault"}
	if _, err := exports.Selection.Update(ctx, choice, initial.Revision); !errors.Is(err, modelselection.ErrConflict) {
		t.Fatalf("changed directory = %v", err)
	}
	latest, err := exports.Selection.Snapshot(ctx)
	if err != nil || latest.Configured {
		t.Fatalf("removed selection = %#v, err = %v", latest, err)
	}
	// A provider without a declared model list is not selectable by the picker.
	if _, err := exports.Selection.Update(ctx, choice, latest.Revision); !errors.Is(err, modelselection.ErrInvalid) {
		t.Fatalf("undeclared model = %v", err)
	}
	stored, err := loadConfig(scope.Dir())
	if err != nil || stored.DefaultProvider != "first" {
		t.Fatalf("invalid choice persisted: %#v, err = %v", stored, err)
	}
}

func TestModelSelectionCanConfigureAndRepairDefaults(t *testing.T) {
	ctx := context.Background()
	for _, cfg := range []Config{{}, {DefaultProvider: "removed", DefaultModel: "old"}} {
		exports, scope, source := selectionFixture(t)
		if err := saveConfig(scope.Dir(), cfg); err != nil {
			t.Fatal(err)
		}
		exports, _, err := newRuntimeForTest(ctx, Dependencies{State: scope, ProviderSources: []model.ProviderSource{source}})
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := exports.Selection.Snapshot(ctx)
		if err != nil || snapshot.Configured || len(snapshot.Providers) != 2 {
			t.Fatalf("unconfigured = %#v, err = %v", snapshot, err)
		}
		choice := modelselection.Selection{Provider: "second", Model: "new", ReasoningEffort: "low"}
		if _, err := exports.Selection.Update(ctx, choice, snapshot.Revision); err != nil {
			t.Fatal(err)
		}
		assertLiveSelection(t, exports, "second", "new", "second")
	}
}

func TestModelSelectionAndConfigOperationShareConflictDetection(t *testing.T) {
	ctx := context.Background()
	t.Run("operation invalidates picker revision", func(t *testing.T) {
		exports, _, _ := selectionFixture(t)
		initial, err := exports.Selection.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		channel := &setupTestChannel{respond: func(interaction.Request) (interaction.Response, error) {
			return interaction.Response{Values: []interaction.Answer{
				{Name: "default_provider", Value: interaction.StringValue("second")},
				{Name: "default_model", Value: interaction.StringValue("new")},
				{Name: "default_reasoning_effort", Value: interaction.StringValue("low")},
			}}, nil
		}}
		if _, err := exports.Operations[0].Invoke(ctx, operation.Request{Interaction: channel}); err != nil {
			t.Fatal(err)
		}
		if _, err := exports.Selection.Update(ctx, initial.Current, initial.Revision); !errors.Is(err, modelselection.ErrConflict) {
			t.Fatalf("stale picker = %v", err)
		}
	})
	t.Run("picker invalidates pending operation", func(t *testing.T) {
		exports, _, _ := selectionFixture(t)
		initial, err := exports.Selection.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		updated := false
		channel := &setupTestChannel{respond: func(request interaction.Request) (interaction.Response, error) {
			if !updated {
				updated = true
				_, err := exports.Selection.Update(ctx, modelselection.Selection{Provider: "second", Model: "new", ReasoningEffort: "low"}, initial.Revision)
				if err != nil {
					t.Fatal(err)
				}
			}
			return defaultsResponse(request)
		}}
		if _, err := exports.Operations[0].Invoke(ctx, operation.Request{Interaction: channel}); !errors.Is(err, ErrConfigConflict) {
			t.Fatalf("stale operation = %v", err)
		}
		assertLiveSelection(t, exports, "second", "new", "second")
	})
}

func TestModelSelectionRejectsExternalEditAndCancellation(t *testing.T) {
	ctx := context.Background()
	exports, scope, _ := selectionFixture(t)
	initial, err := exports.Selection.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	choice := modelselection.Selection{Provider: "second", Model: "new", ReasoningEffort: "low"}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := exports.Selection.Update(canceled, choice, initial.Revision); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled update = %v", err)
	}
	external := Config{DefaultProvider: "second", DefaultModel: "external"}
	if err := saveConfig(scope.Dir(), external); err != nil {
		t.Fatal(err)
	}
	if _, err := exports.Selection.Update(ctx, choice, initial.Revision); !errors.Is(err, modelselection.ErrConflict) {
		t.Fatalf("external edit = %v", err)
	}
	stored, err := loadConfig(scope.Dir())
	if err != nil || stored != external {
		t.Fatalf("external edit overwritten: %#v, err = %v", stored, err)
	}
	active, err := exports.Selection.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(initial, active) {
		t.Fatalf("failed update changed active defaults: %#v, err = %v", active, err)
	}
}

func TestConcurrentModelSelectionsOnlyCommitOneRevision(t *testing.T) {
	ctx := context.Background()
	exports, _, _ := selectionFixture(t)
	initial, err := exports.Selection.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for _, effort := range []string{"low", "providerDefault"} {
		go func(effort string) {
			_, err := exports.Selection.Update(ctx, modelselection.Selection{Provider: "second", Model: "new", ReasoningEffort: effort}, initial.Revision)
			results <- err
		}(effort)
	}
	successes, conflicts := 0, 0
	for i := 0; i < 2; i++ {
		err := <-results
		switch {
		case err == nil:
			successes++
		case errors.Is(err, modelselection.ErrConflict):
			conflicts++
		default:
			t.Fatalf("concurrent update = %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes = %d, conflicts = %d", successes, conflicts)
	}
}
