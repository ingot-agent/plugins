package usagedefault_test

import (
	"context"
	"testing"

	"github.com/ingot-agent/ingot-abi"
	usagedefault "github.com/ingot-agent/plugins/usage-default"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/usage"
)

type contractResolver struct{}

func (contractResolver) ResolveRequest(_ context.Context, request model.Request) (model.Request, error) {
	if request.Provider == "" {
		request.Provider = "provider"
	}
	if request.Model == "" {
		request.Model = "model"
	}
	return request, nil
}

func TestPublicComponentContract(t *testing.T) {
	t.Parallel()
	var constructor func(context.Context, usagedefault.Dependencies) (usagedefault.Exports, ingotabi.Cleanup, error) = usagedefault.New
	_ = constructor
	exports, cleanup, err := usagedefault.New(context.Background(), withState(t, usagedefault.Config{}, usagedefault.Dependencies{Resolver: contractResolver{}}))
	if err != nil {
		t.Fatal(err)
	}
	if cleanup == nil {
		t.Fatal("cleanup is nil")
	}
	var _ usage.Counter = exports.Counter
	if err := cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
}
