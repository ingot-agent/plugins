package openairesponses

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/pelletier/go-toml/v2"
)

// configFileName is the Plugin-owned configuration file inside this Plugin's
// Runtime state scope. The runtime never reads or decodes it; the file name
// and its contents are this Plugin's private persistent contract.
const configFileName = "config.toml"

var configCommitMu sync.Mutex

// loadConfig reads the Plugin-owned configuration from its Runtime state
// scope. A missing file is the normal Unconfigured state and yields the
// zero-value Config so defaults apply; any other read or decode failure is
// reported instead of silently discarding persisted state.
func loadConfig(scope string) (Config, error) {
	var config Config
	if scope == "" {
		return config, nil
	}
	data, err := os.ReadFile(filepath.Join(scope, configFileName))
	if errors.Is(err, os.ErrNotExist) {
		return config, nil
	}
	if err != nil {
		return config, fmt.Errorf("read plugin config: %w", err)
	}
	decoder := toml.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return config, fmt.Errorf("decode plugin config: %w", err)
	}
	return config, nil
}

// saveConfig atomically writes the Plugin-owned configuration into its Runtime
// state scope, creating the scope when needed. The Host never participates: the
// Operation that triggers a change calls this directly.
func saveConfig(scope string, config Config) error {
	if scope == "" {
		return fmt.Errorf("state scope is required: %w", ErrInvalidConfig)
	}
	if err := os.MkdirAll(scope, 0o700); err != nil {
		return fmt.Errorf("create state scope: %w", err)
	}
	data, err := toml.Marshal(config)
	if err != nil {
		return fmt.Errorf("encode plugin config: %w", err)
	}
	temporary, err := os.CreateTemp(scope, ".config-")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, filepath.Join(scope, configFileName))
}

// validateProviders applies the same provider rules at construction and in the
// setup Operation, so a persisted list is always one the Plugin would accept at
// startup. It validates the shape that does not need runtime dependencies.
func validateProviders(config Config) error {
	_, err := normalizeProviders(config)
	return err
}

func normalizeProviders(config Config) ([]normalizedProviderConfig, error) {
	if len(config.Providers) == 0 {
		return nil, configError("providers", "must contain at least one provider")
	}
	seen := make(map[string]struct{}, len(config.Providers))
	normalized := make([]normalizedProviderConfig, 0, len(config.Providers))
	for i, candidate := range config.Providers {
		provider, err := normalizeProviderConfig(candidate)
		if err != nil {
			return nil, fmt.Errorf("providers[%d]: %w", i, err)
		}
		if _, exists := seen[provider.name]; exists {
			return nil, fmt.Errorf("providers[%d]: %w", i, configError("name", "must be unique"))
		}
		seen[provider.name] = struct{}{}
		normalized = append(normalized, provider)
	}
	return normalized, nil
}
