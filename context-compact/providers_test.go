package contextcompact

import (
	"context"
	"errors"
	"testing"

	"github.com/ingot-agent/sdk/model"
)

type setupProvider struct{}

func (setupProvider) Complete(context.Context, model.Request) (model.Response, error) {
	return model.Response{}, nil
}

type setupProviderSource struct {
	names []string
	err   error
	calls int
}

func (s *setupProviderSource) Snapshot(ctx context.Context) ([]model.ProviderEntry, error) {
	s.calls++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.err != nil {
		return nil, s.err
	}
	entries := make([]model.ProviderEntry, len(s.names))
	for i, name := range s.names {
		entries[i] = model.ProviderEntry{Name: name, Complete: setupProvider{}.Complete}
	}
	return entries, nil
}

func TestNewValidatesSourcesWithoutResolvingDynamicProviders(t *testing.T) {
	var typedNil *setupProviderSource
	for _, test := range []struct {
		name    string
		sources []model.ProviderSource
		wantErr bool
	}{
		{name: "missing configured provider", sources: []model.ProviderSource{&setupProviderSource{}}},
		{name: "duplicate names", sources: []model.ProviderSource{&setupProviderSource{names: []string{"duplicate", "duplicate"}}}},
		{name: "temporarily unavailable", sources: []model.ProviderSource{&setupProviderSource{err: errors.New("unavailable")}}},
		{name: "nil source", sources: []model.ProviderSource{nil}, wantErr: true},
		{name: "typed nil source", sources: []model.ProviderSource{typedNil}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			sources := test.sources
			exports, cleanup, err := New(context.Background(), withState(t, Config{Provider: "removed"}, Dependencies{Model: setupProvider{}, Store: &memoryStore{}, ProviderSources: sources}))
			if test.wantErr {
				if !errors.Is(err, ErrInvalidConfig) {
					t.Fatalf("New error = %v, want ErrInvalidConfig", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cleanup != nil {
				t.Cleanup(func() { _ = cleanup(context.Background()) })
			}
			if len(exports.Operations) != 1 || sources[0].(*setupProviderSource).calls != 0 {
				t.Fatal("constructor must expose config without reading dynamic providers")
			}
		})
	}
}

func TestCurrentProviderNamesRejectsDuplicateAcrossSources(t *testing.T) {
	_, err := currentProviderNames(context.Background(), []model.ProviderSource{
		&setupProviderSource{names: []string{"same"}},
		&setupProviderSource{names: []string{"same"}},
	})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error = %v, want ErrInvalidConfig", err)
	}
}

type setupProviderEntries []model.ProviderEntry

func (entries setupProviderEntries) Snapshot(ctx context.Context) ([]model.ProviderEntry, error) {
	return append([]model.ProviderEntry(nil), entries...), ctx.Err()
}

func TestCurrentProviderNamesValidatesEntryCalls(t *testing.T) {
	for _, test := range []struct {
		name    string
		entry   model.ProviderEntry
		wantErr bool
	}{
		{name: "complete without stream", entry: model.ProviderEntry{Name: "valid", Complete: setupProvider{}.Complete}},
		{name: "missing complete", entry: model.ProviderEntry{Name: "invalid"}, wantErr: true},
		{name: "empty name", entry: model.ProviderEntry{Complete: setupProvider{}.Complete}, wantErr: true},
		{name: "invalid name encoding", entry: model.ProviderEntry{Name: string([]byte{0xff}), Complete: setupProvider{}.Complete}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			names, err := currentProviderNames(context.Background(), []model.ProviderSource{setupProviderEntries{test.entry}})
			if test.wantErr {
				if !errors.Is(err, ErrInvalidConfig) {
					t.Fatalf("error = %v, want ErrInvalidConfig", err)
				}
				return
			}
			if err != nil || len(names) != 1 || names[0] != test.entry.Name {
				t.Fatalf("names = %#v, error = %v", names, err)
			}
		})
	}
}
