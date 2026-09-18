package openaicompat

import (
	"context"
	"slices"
	"sync/atomic"

	"github.com/ingot-agent/sdk/asset"
	"github.com/ingot-agent/sdk/httpx"
	"github.com/ingot-agent/sdk/model"
)

type providerSnapshot struct {
	entries []model.ProviderEntry
}

type providerSource struct {
	http    httpx.Client
	assets  asset.Resolver
	current atomic.Pointer[providerSnapshot]
}

var _ model.ProviderSource = (*providerSource)(nil)

func (s *providerSource) Snapshot(ctx context.Context) ([]model.ProviderEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return slices.Clone(s.current.Load().entries), nil
}

func (s *providerSource) prepare(configs []normalizedProviderConfig) *providerSnapshot {
	entries := make([]model.ProviderEntry, 0, len(configs))
	for _, config := range configs {
		instance := newProviderFromNormalized(config, s.http, s.assets)
		entries = append(entries, model.ProviderEntry{Name: instance.name, Complete: instance.Complete, Stream: instance.Stream})
	}
	return &providerSnapshot{entries: entries}
}
