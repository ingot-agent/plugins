package modelruntime

import (
	"context"

	ingotabi "github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/session"
)

type testSessions struct{ session.Manager }

func (testSessions) Get(ctx context.Context, id session.ID) (session.Metadata, error) {
	return session.Metadata{ID: id}, ctx.Err()
}

func (testSessions) AddTotalTokens(ctx context.Context, ids []session.ID, delta int64) ([]session.Metadata, error) {
	result := []session.Metadata{}
	for _, id := range ids {
		result = append(result, session.Metadata{ID: id, TotalToken: delta})
	}
	return result, ctx.Err()
}

func newRuntimeForTest(ctx context.Context, deps Dependencies) (Exports, ingotabi.Cleanup, error) {
	deps.Sessions, deps.TokenUsage = testSessions{}, testSessions{}
	return New(ctx, deps)
}
