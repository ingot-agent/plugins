package contextcompact

import (
	"context"
	"fmt"
	"unicode/utf8"

	"github.com/ingot-agent/sdk/model"
)

func currentProviderNames(ctx context.Context, sources []model.ProviderSource) ([]string, error) {
	names := make([]string, 0)
	seen := make(map[string]struct{})
	for i, source := range sources {
		if isNil(source) {
			return nil, fmt.Errorf("provider_sources[%d] is nil: %w", i, ErrInvalidConfig)
		}
		entries, err := source.Snapshot(ctx)
		if err != nil {
			return nil, fmt.Errorf("provider_sources[%d]: %w", i, err)
		}
		for _, entry := range entries {
			if entry.Name == "" || !utf8.ValidString(entry.Name) || entry.Complete == nil {
				return nil, fmt.Errorf("provider_sources[%d] contains an invalid provider: %w", i, ErrInvalidConfig)
			}
			if _, exists := seen[entry.Name]; exists {
				return nil, fmt.Errorf("duplicate provider %q: %w", entry.Name, ErrInvalidConfig)
			}
			seen[entry.Name] = struct{}{}
			names = append(names, entry.Name)
		}
	}
	return names, nil
}
