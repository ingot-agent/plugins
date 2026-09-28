package agentdefault

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/plugins/app-webui/modelselection"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
)

type selectionSource struct{ entries []model.ProviderEntry }

func (s selectionSource) Snapshot(context.Context) ([]model.ProviderEntry, error) {
	return append([]model.ProviderEntry(nil), s.entries...), nil
}

type selectionResolver struct{}

func (selectionResolver) ResolveRequest(_ context.Context, request model.Request) (model.Request, error) {
	if request.Provider == "" {
		request.Provider = "first"
	}
	if request.Model == "" {
		request.Model = "old"
	}
	if request.ReasoningEffort == model.ReasoningEffortProviderDefault {
		request.ReasoningEffort = ""
	} else if request.ReasoningEffort == "" {
		request.ReasoningEffort = model.ReasoningEffortHigh
	}
	return request, nil
}

func TestModelSelectionUpdatesFutureRequests(t *testing.T) {
	ctx := context.Background()
	complete := func(context.Context, model.Request) (model.Response, error) { return model.Response{}, nil }
	source := selectionSource{entries: []model.ProviderEntry{
		{Name: "first", Models: []model.ModelEntry{{Name: "old", ReasoningEfforts: []model.ReasoningEffort{model.ReasoningEffortHigh}}}, Complete: complete},
		{Name: "second", Models: []model.ModelEntry{{Name: "new", ReasoningEfforts: []model.ReasoningEffort{model.ReasoningEffortLow}}}, Complete: complete},
	}}
	configuration, err := normalizeConfig(Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	modelRuntime := &sequenceModel{responses: []model.Response{
		{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("one")}},
		{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("two")}},
	}}
	r := &runtime{
		model: modelRuntime, selectionState: setupTestScope{dir: t.TempDir()},
		selectionSources: []model.ProviderSource{source},
		resolver:         ingotabi.Optional[model.RequestResolver]{Valid: true, Value: selectionResolver{}},
	}
	r.config.Store(&configuration)
	initial, err := r.Snapshot(ctx)
	if err != nil || !initial.Configured || initial.Current.Provider != "first" || initial.Current.ReasoningEffort != "high" {
		t.Fatalf("initial = %#v, err = %v", initial, err)
	}
	choice := modelselection.Selection{Provider: "second", Model: "new", ReasoningEffort: "low"}
	if _, err := r.Update(ctx, modelselection.Selection{Provider: "second", Model: "new", ReasoningEffort: "high"}, initial.Revision); !errors.Is(err, modelselection.ErrInvalid) {
		t.Fatalf("unsupported effort error = %v", err)
	}
	selected, err := r.Update(ctx, choice, initial.Revision)
	if err != nil || selected.Current != choice {
		t.Fatalf("selected = %#v, err = %v", selected, err)
	}
	if _, err := r.Update(ctx, choice, initial.Revision); !errors.Is(err, modelselection.ErrConflict) {
		t.Fatalf("stale revision error = %v", err)
	}
	invoke := func() {
		t.Helper()
		callCtx := withExecutionRecorder(ctx, newExecutionRecorder(discardObservation{}))
		if _, err := r.invokeRoundModel(callCtx, "session", 0, nil, nil, nil, *r.config.Load()); err != nil {
			t.Fatal(err)
		}
	}
	invoke()
	if request := modelRuntime.requests[0]; request.Provider != "second" || request.Model != "new" || request.ReasoningEffort != model.ReasoningEffortLow {
		t.Fatalf("explicit request = %#v", request)
	}
	providerDefault := modelselection.Selection{Provider: "second", Model: "new", ReasoningEffort: string(model.ReasoningEffortProviderDefault)}
	if _, err := r.Update(ctx, providerDefault, selected.Revision); err != nil {
		t.Fatal(err)
	}
	invoke()
	if request := modelRuntime.requests[1]; request.ReasoningEffort != model.ReasoningEffortProviderDefault {
		t.Fatalf("provider-default request = %#v", request)
	}
	stored, err := loadConfig(r.selectionState.Dir())
	if err != nil || stored.Provider != "second" || stored.Model != "new" || stored.ReasoningEffort != model.ReasoningEffortProviderDefault {
		t.Fatalf("stored = %#v, err = %v", stored, err)
	}
}

func TestLegacyProviderDefaultReasoningConfigMigrates(t *testing.T) {
	scope := t.TempDir()
	path := filepath.Join(scope, configFileName)
	if err := os.WriteFile(path, []byte("provider = 'p'\nmodel = 'm'\nprovider_default_reasoning = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadConfig(scope)
	if err != nil || loaded.ReasoningEffort != model.ReasoningEffortProviderDefault {
		t.Fatalf("legacy config = %#v, err = %v", loaded, err)
	}
	if err := saveConfig(scope, loaded); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(raw), "provider_default_reasoning") || !strings.Contains(string(raw), "providerDefault") {
		t.Fatalf("migrated config = %q, err = %v", raw, err)
	}
}
