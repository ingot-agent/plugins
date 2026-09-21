package contextcompact_test

import (
	"context"
	"testing"

	"github.com/ingot-agent/ingot-abi"
	contextcompact "github.com/ingot-agent/plugins/context-compact"
	"github.com/ingot-agent/sdk/contextwindow"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/usage"
)

type contractModel struct{}

func (contractModel) Complete(context.Context, model.Request) (model.Response, error) {
	return model.Response{}, nil
}

type contractCounter struct{}

func (contractCounter) CountInput(context.Context, usage.CountRequest) (usage.CountResult, error) {
	return usage.CountResult{Accuracy: usage.AccuracyExact, Source: "contract-counter", Provider: "p", Model: "m"}, nil
}

type contractStore struct{}

func (contractStore) Create(context.Context, session.CreateRequest) (session.Metadata, error) {
	return session.Metadata{}, nil
}
func (contractStore) Append(context.Context, session.ID, session.Entry) error   { return nil }
func (contractStore) Load(context.Context, session.ID) ([]session.Entry, error) { return nil, nil }

func TestPublicComponentContract(t *testing.T) {
	t.Parallel()
	exports, cleanup, err := contextcompact.New(context.Background(), withState(t, contextcompact.Config{TriggerInputTokens: 1024, TargetInputTokens: 512}, contextcompact.Dependencies{Model: contractModel{}, Store: contractStore{}}))
	if err != nil {
		t.Fatal(err)
	}
	if cleanup != nil {
		t.Fatal("cleanup must be nil")
	}
	var _ contextwindow.Compactor = exports.Compactor
}

var _ func(context.Context, contextcompact.Dependencies) (contextcompact.Exports, ingotabi.Cleanup, error) = contextcompact.New
