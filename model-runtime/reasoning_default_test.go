package modelruntime

import (
	"context"
	"errors"
	"testing"

	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
)

func TestExplicitProviderDefaultSuppressesRuntimeReasoningDefault(t *testing.T) {
	ctx := context.Background()
	scope := setupTestScope{dir: t.TempDir()}
	if err := saveConfig(scope.Dir(), Config{DefaultProvider: "p", DefaultModel: "m", DefaultReasoningEffort: model.ReasoningEffortHigh}); err != nil {
		t.Fatal(err)
	}
	var observed model.Request
	source := &liveSource{}
	source.set(model.ProviderEntry{
		Name: "p", Models: []model.ModelEntry{{Name: "m", ReasoningEfforts: []model.ReasoningEffort{model.ReasoningEffortHigh}}},
		Complete: func(_ context.Context, request model.Request) (model.Response, error) {
			observed = request
			return model.Response{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("ok")}}, nil
		},
	})
	exports, _, err := New(ctx, Dependencies{State: scope, ProviderSources: []model.ProviderSource{source}})
	if err != nil {
		t.Fatal(err)
	}
	request := model.Request{ReasoningEffort: model.ReasoningEffortProviderDefault}
	resolved, err := exports.Resolver.ResolveRequest(ctx, request)
	if err != nil || resolved.ReasoningEffort != "" {
		t.Fatalf("resolved = %#v, err = %v", resolved, err)
	}
	if _, err := exports.Runtime.Complete(ctx, request); err != nil || observed.ReasoningEffort != "" {
		t.Fatalf("provider request = %#v, err = %v", observed, err)
	}
	if resolved, err := exports.Resolver.ResolveRequest(ctx, model.Request{}); err != nil || resolved.ReasoningEffort != model.ReasoningEffortHigh {
		t.Fatalf("inherited reasoning default = %#v, err = %v", resolved, err)
	}
	if _, err := exports.Resolver.ResolveRequest(ctx, model.Request{ReasoningEffort: model.ReasoningEffortLow}); !errors.Is(err, model.ErrReasoningEffortUnsupported) {
		t.Fatalf("unsupported effort error = %v", err)
	}
}
