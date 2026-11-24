package contextcompact

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/ingot-agent/sdk/logging"
	"github.com/ingot-agent/sdk/model"
	"github.com/pelletier/go-toml/v2"
)

// configFileName is the Plugin-owned configuration file inside this Plugin's
// Runtime state scope. The runtime never reads or decodes it.
const configFileName = "config.toml"

var configCommitMu sync.Mutex

// Config is the entire operator-facing surface: where to summarize, how much to
// keep, and how much room a summary may take. Everything else that v1 exposed
// as a watermark is either derived or a fixed constant.
type Config struct {
	Provider            string `toml:"provider"`
	Model               string `toml:"model"`
	TriggerTokens       int64  `toml:"trigger_tokens"`
	TargetTokens        int64  `toml:"target_tokens"`
	ProtectRecentTokens int64  `toml:"protect_recent_tokens"`
	SummaryMaxTokens    int    `toml:"summary_max_tokens"`
}

// normalizedConfig is the validated form used by compaction. The reasoning
// effort is not operator-configurable on purpose: a summarizer that spends its
// output budget thinking returns no text at all, and that failure mode is the
// single largest source of v1 breakage.
type normalizedConfig struct {
	provider            string
	model               string
	triggerTokens       int64
	targetTokens        int64
	protectRecentTokens int64
	summaryMaxTokens    int
	summaryMaxBytes     int
	reasoningEffort     model.ReasoningEffort
}

func normalizeConfig(cfg Config) (normalizedConfig, error) {
	normalized := normalizedConfig{
		provider:            cfg.Provider,
		model:               cfg.Model,
		triggerTokens:       cfg.TriggerTokens,
		targetTokens:        cfg.TargetTokens,
		protectRecentTokens: cfg.ProtectRecentTokens,
		summaryMaxTokens:    cfg.SummaryMaxTokens,
		summaryMaxBytes:     defaultSummaryMaxBytes,
		reasoningEffort:     model.ReasoningEffortNone,
	}
	if normalized.triggerTokens < 0 || normalized.targetTokens < 0 || normalized.protectRecentTokens < 0 {
		return normalizedConfig{}, fmt.Errorf("token settings must not be negative: %w", ErrInvalidConfig)
	}
	if normalized.summaryMaxTokens < 0 {
		return normalizedConfig{}, fmt.Errorf("summary_max_tokens must not be negative: %w", ErrInvalidConfig)
	}
	if normalized.triggerTokens == 0 {
		normalized.triggerTokens = defaultTriggerTokens
	}
	if normalized.targetTokens == 0 {
		normalized.targetTokens = defaultTargetTokens
	}
	if normalized.protectRecentTokens == 0 {
		normalized.protectRecentTokens = defaultProtectRecentTokens
	}
	if normalized.summaryMaxTokens == 0 {
		normalized.summaryMaxTokens = defaultSummaryMaxTokens
	}
	if normalized.targetTokens >= normalized.triggerTokens {
		return normalizedConfig{}, fmt.Errorf("target_tokens must be less than trigger_tokens: %w", ErrInvalidConfig)
	}
	return normalized, nil
}

// currentKeys are the fields this version understands.
var currentKeys = map[string]struct{}{
	"provider": {}, "model": {}, "trigger_tokens": {}, "target_tokens": {},
	"protect_recent_tokens": {}, "summary_max_tokens": {},
}

// legacyKeys maps every field the previous version exposed to its replacement,
// or to "" when it no longer exists. Legacy files are migrated rather than
// rejected: refusing to load one would make the Plugin fail construction, and a
// configuration problem must never stop the agent from starting.
var legacyKeys = map[string]string{
	"trigger_input_tokens":      "trigger_tokens",
	"target_input_tokens":       "target_tokens",
	"summary_max_tokens":        "summary_max_tokens",
	"summary_max_bytes":         "",
	"summary_chunk_tokens":      "",
	"summary_input_tokens":      "",
	"rollup_max_tokens":         "",
	"recent_rounds":             "",
	"memory_trigger_tokens":     "",
	"memory_target_tokens":      "",
	"state_trigger_tokens":      "",
	"state_target_tokens":       "",
	"max_summary_passes":        "",
	"allowed_accuracies":        "",
	"token_count_cache_entries": "",
}

// toConfig renders the effective settings as a persistable configuration.
func (c normalizedConfig) toConfig() Config {
	return Config{
		Provider:            c.provider,
		Model:               c.model,
		TriggerTokens:       c.triggerTokens,
		TargetTokens:        c.targetTokens,
		ProtectRecentTokens: c.protectRecentTokens,
		SummaryMaxTokens:    c.summaryMaxTokens,
	}
}

// loadConfig reads the Plugin-owned configuration. A missing file is the normal
// Unconfigured state and yields defaults. A file written by the previous version
// is translated and reported; any other decode failure is returned.
func loadConfig(scope string) (Config, bool, error) {
	var config Config
	if scope == "" {
		return config, false, nil
	}
	data, err := os.ReadFile(filepath.Join(scope, configFileName))
	if errors.Is(err, os.ErrNotExist) {
		return config, false, nil
	}
	if err != nil {
		return config, false, fmt.Errorf("read plugin config: %w", err)
	}
	decoder := toml.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err == nil {
		return config, false, nil
	}
	migrated, dropped, err := migrateLegacyConfig(data)
	if err != nil {
		return Config{}, false, err
	}
	_ = dropped
	return migrated, true, nil
}

// migrateLegacyConfig translates a previous-version file. It reports the fields
// it had to discard, and refuses only when a field is unknown to both versions:
// that is almost always a typo worth surfacing at startup.
func migrateLegacyConfig(data []byte) (Config, []string, error) {
	var fields map[string]any
	if err := toml.Unmarshal(data, &fields); err != nil {
		return Config{}, nil, fmt.Errorf("decode plugin config: %w", err)
	}
	projected := make(map[string]any, len(fields))
	dropped := make([]string, 0)
	for name, value := range fields {
		if _, ok := currentKeys[name]; ok {
			projected[name] = value
			continue
		}
		target, legacy := legacyKeys[name]
		if !legacy {
			return Config{}, nil, fmt.Errorf("decode plugin config: unknown field %q", name)
		}
		if target == "" {
			dropped = append(dropped, name)
			continue
		}
		if _, exists := projected[target]; !exists {
			projected[target] = value
		}
	}
	encoded, err := toml.Marshal(projected)
	if err != nil {
		return Config{}, nil, fmt.Errorf("encode migrated plugin config: %w", err)
	}
	var config Config
	decoder := toml.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return Config{}, nil, fmt.Errorf("decode migrated plugin config: %w", err)
	}
	return config, dropped, nil
}

// saveConfig atomically writes the configuration, creating the scope when
// needed.
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

// fileStamp detects a config rewrite without parsing it.
type fileStamp struct {
	modTime int64
	size    int64
	present bool
}

func statConfig(scope string) fileStamp {
	if scope == "" {
		return fileStamp{}
	}
	info, err := os.Stat(filepath.Join(scope, configFileName))
	if err != nil {
		return fileStamp{}
	}
	return fileStamp{modTime: info.ModTime().UnixNano(), size: info.Size(), present: true}
}

// configLoader keeps the active configuration and reloads it when the file
// changes on disk. v1 read the file only during construction, so editing it had
// no effect until the runtime restarted, which made every tuning attempt look
// like it had been ignored.
type configLoader struct {
	scope   string
	logger  logging.Logger
	current atomic.Pointer[normalizedConfig]

	mu     sync.Mutex
	stamp  fileStamp
	loaded bool
}

func newConfigLoader(scope string, logger logging.Logger) (*configLoader, error) {
	loader := &configLoader{scope: scope, logger: logger, stamp: statConfig(scope)}
	cfg, migrated, err := loadConfig(scope)
	if err != nil {
		return nil, fmt.Errorf("load plugin config: %w: %w", err, ErrInvalidConfig)
	}
	normalized, err := normalizeConfig(cfg)
	if err != nil {
		return nil, err
	}
	if migrated && scope != "" {
		// Rewrite the file in the current shape with the effective values, so the
		// operator sees the settings that are actually in use instead of the
		// zero-means-default placeholders of a partial file.
		if err := saveConfig(scope, normalized.toConfig()); err != nil {
			logger.Warn(context.Background(), "rewrite migrated context.compact config", logging.Err(err))
		} else {
			loader.stamp = statConfig(scope)
		}
		logger.Warn(context.Background(), "migrated legacy context.compact config",
			logging.String("action", "removed fields that no longer exist; review the file"))
	}
	loader.current.Store(&normalized)
	loader.loaded = true
	return loader, nil
}

// load returns the active configuration, reloading first if the file changed.
// A broken edit keeps the previous configuration and logs once; compaction must
// not fail because an operator typed a bad value.
func (l *configLoader) load() normalizedConfig {
	stamp := statConfig(l.scope)
	if stamp == l.stamp {
		return *l.current.Load()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if stamp = statConfig(l.scope); stamp == l.stamp {
		return *l.current.Load()
	}
	l.stamp = stamp
	cfg, migrated, err := loadConfig(l.scope)
	if err != nil {
		l.logger.Warn(context.Background(), "reload context.compact config", logging.Err(err))
		return *l.current.Load()
	}
	if migrated && l.scope != "" {
		if normalized, normalizeErr := normalizeConfig(cfg); normalizeErr == nil {
			cfg = normalized.toConfig()
		}
		if err := saveConfig(l.scope, cfg); err != nil {
			l.logger.Warn(context.Background(), "rewrite migrated context.compact config", logging.Err(err))
		} else {
			l.stamp = statConfig(l.scope)
		}
	}
	normalized, err := normalizeConfig(cfg)
	if err != nil {
		l.logger.Warn(context.Background(), "reload context.compact config", logging.Err(err))
		return *l.current.Load()
	}
	l.current.Store(&normalized)
	return normalized
}

// snapshot returns the current configuration without touching the filesystem.
func (l *configLoader) snapshot() normalizedConfig { return *l.current.Load() }

// update replaces the active configuration after an Operation persists it.
func (l *configLoader) update(cfg Config) (normalizedConfig, error) {
	normalized, err := normalizeConfig(cfg)
	if err != nil {
		return normalizedConfig{}, err
	}
	l.mu.Lock()
	l.stamp = statConfig(l.scope)
	l.mu.Unlock()
	l.current.Store(&normalized)
	return normalized, nil
}
