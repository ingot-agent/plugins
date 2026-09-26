// Package contextcompact implements incremental, session-scoped model context
// compaction while preserving the original session history.
package contextcompact

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"unicode/utf8"

	"github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/ingot-abi/state"
	"github.com/ingot-agent/sdk/contextwindow"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/operation"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/usage"
)

const (
	defaultTriggerInputTokens  = 800000
	defaultTargetInputTokens   = 250000
	defaultRecentRounds        = 4
	defaultSummaryChunkTokens  = 128000
	defaultSummaryInputTokens  = 256000
	defaultSummaryMaxTokens    = 1024
	defaultRollupMaxTokens     = 4096
	defaultSummaryMaxBytes     = 64 * 1024
	defaultMemoryTriggerTokens = 16384
	defaultMemoryTargetTokens  = 8192
	defaultStateTriggerTokens  = 4096
	defaultStateTargetTokens   = 2048
	defaultMaxSummaryPasses    = 8
)

var (
	ErrInvalidConfig                = errors.New("invalid context.compact config")
	ErrInvalidRequest               = errors.New("invalid context compaction request")
	ErrInvalidHistory               = errors.New("invalid context history")
	ErrUnsupportedCheckpointVersion = errors.New("unsupported context checkpoint version")
	ErrCorruptCheckpoint            = errors.New("corrupt context checkpoint")
	ErrCompactionFailed             = errors.New("context compaction failed")
	ErrContextUncompactable         = errors.New("context cannot be compacted to target")
	ErrInvalidCount                 = errors.New("invalid or inconsistent token count")
	ErrUnsupportedAccuracy          = errors.New("token count accuracy is not allowed")
)

// Config controls input-token watermarks, softly preserved rounds, and memory bounds.
// Zero numeric values use defaults. SummaryMaxBytes is a data-size guard only.
type Config struct {
	Provider            string           `toml:"provider"`
	Model               string           `toml:"model"`
	TriggerInputTokens  int64            `toml:"trigger_input_tokens"`
	TargetInputTokens   int64            `toml:"target_input_tokens"`
	RecentRounds        int              `toml:"recent_rounds"`
	SummaryChunkTokens  int64            `toml:"summary_chunk_tokens"`
	SummaryInputTokens  int64            `toml:"summary_input_tokens"`
	SummaryMaxTokens    int              `toml:"summary_max_tokens"`
	RollupMaxTokens     int              `toml:"rollup_max_tokens"`
	SummaryMaxBytes     int              `toml:"summary_max_bytes"`
	MemoryTriggerTokens int64            `toml:"memory_trigger_tokens"`
	MemoryTargetTokens  int64            `toml:"memory_target_tokens"`
	StateTriggerTokens  int64            `toml:"state_trigger_tokens"`
	StateTargetTokens   int64            `toml:"state_target_tokens"`
	MaxSummaryPasses    int              `toml:"max_summary_passes"`
	AllowedAccuracies   []usage.Accuracy `toml:"allowed_accuracies,omitempty"`
}

// Dependencies contains the model and counting capabilities and append-oriented store.
type Dependencies struct {
	Model           model.Runtime
	Counter         ingotabi.Optional[usage.Counter]
	Resolver        ingotabi.Optional[model.RequestResolver]
	ProviderSources []model.ProviderSource
	Store           session.Store
	State           state.Scope
}

type Exports struct {
	Compactor  contextwindow.Compactor
	Operations []operation.Operation
}

type normalizedConfig struct {
	provider, model                                    string
	triggerInputTokens, targetInputTokens              int64
	recentRounds                                       int
	summaryChunkTokens, summaryInputTokens             int64
	summaryMaxTokens, rollupMaxTokens, summaryMaxBytes int
	memoryTriggerTokens, memoryTargetTokens            int64
	stateTriggerTokens, stateTargetTokens              int64
	maxSummaryPasses                                   int
	allowedAccuracies                                  uint8
}

type compactor struct {
	model    model.Runtime
	counter  usage.Counter
	resolver model.RequestResolver
	store    session.Store
	cfg      normalizedConfig
	config   atomic.Pointer[normalizedConfig]
	gates    *gateManager
}

func New(ctx context.Context, deps Dependencies) (Exports, ingotabi.Cleanup, error) {
	if ctx == nil {
		return Exports{}, nil, fmt.Errorf("construct context.compact: nil context: %w", ErrInvalidConfig)
	}
	if err := ctx.Err(); err != nil {
		return Exports{}, nil, err
	}
	if isNil(deps.Model) || isNil(deps.Store) || isNil(deps.State) {
		return Exports{}, nil, fmt.Errorf("model, store, and state dependencies are required: %w", ErrInvalidConfig)
	}
	for i, source := range deps.ProviderSources {
		if isNil(source) {
			return Exports{}, nil, fmt.Errorf("provider_sources[%d] is nil: %w", i, ErrInvalidConfig)
		}
	}
	cfg, err := loadConfig(deps.State.Dir())
	if err != nil {
		return Exports{}, nil, fmt.Errorf("construct context.compact: %w: %w", err, ErrInvalidConfig)
	}
	normalized, err := normalizeConfig(cfg)
	if err != nil {
		return Exports{}, nil, err
	}
	var counter usage.Counter
	if deps.Counter.Valid {
		if isNil(deps.Counter.Value) {
			return Exports{}, nil, fmt.Errorf("counter is typed nil: %w", ErrInvalidConfig)
		}
		counter = deps.Counter.Value
	}
	var resolver model.RequestResolver
	if deps.Resolver.Valid {
		if isNil(deps.Resolver.Value) {
			return Exports{}, nil, fmt.Errorf("resolver is typed nil: %w", ErrInvalidConfig)
		}
		resolver = deps.Resolver.Value
	}
	instance := &compactor{model: deps.Model, counter: counter, resolver: resolver, store: deps.Store, cfg: normalized, gates: newGateManager()}
	instance.config.Store(&normalized)
	return Exports{Compactor: instance, Operations: []operation.Operation{&setupOperation{scope: deps.State, providerSources: append([]model.ProviderSource(nil), deps.ProviderSources...), compactor: instance}}}, nil, nil
}

func normalizeConfig(cfg Config) (normalizedConfig, error) {
	return normalizeConfigForProviders(cfg, nil)
}

func normalizeConfigForProviders(cfg Config, providerNames []string) (normalizedConfig, error) {
	if !utf8.ValidString(cfg.Provider) || !utf8.ValidString(cfg.Model) {
		return normalizedConfig{}, fmt.Errorf("invalid provider or model: %w", ErrInvalidConfig)
	}
	if cfg.Provider != "" && providerNames != nil {
		available := false
		for _, name := range providerNames {
			if cfg.Provider == name {
				available = true
				break
			}
		}
		if !available {
			return normalizedConfig{}, fmt.Errorf("provider %q is unavailable: %w", cfg.Provider, ErrInvalidConfig)
		}
	}
	for _, field := range []struct {
		name     string
		value    *int64
		fallback int64
	}{
		{"trigger_input_tokens", &cfg.TriggerInputTokens, defaultTriggerInputTokens},
		{"target_input_tokens", &cfg.TargetInputTokens, defaultTargetInputTokens},
		{"summary_chunk_tokens", &cfg.SummaryChunkTokens, defaultSummaryChunkTokens},
		{"summary_input_tokens", &cfg.SummaryInputTokens, defaultSummaryInputTokens},
		{"memory_trigger_tokens", &cfg.MemoryTriggerTokens, defaultMemoryTriggerTokens},
		{"memory_target_tokens", &cfg.MemoryTargetTokens, defaultMemoryTargetTokens},
		{"state_trigger_tokens", &cfg.StateTriggerTokens, defaultStateTriggerTokens},
		{"state_target_tokens", &cfg.StateTargetTokens, defaultStateTargetTokens},
	} {
		if *field.value < 0 {
			return normalizedConfig{}, fmt.Errorf("%s must be positive: %w", field.name, ErrInvalidConfig)
		}
		if *field.value == 0 {
			*field.value = field.fallback
		}
	}
	for _, field := range []struct {
		name     string
		value    *int
		fallback int
	}{
		{"recent_rounds", &cfg.RecentRounds, defaultRecentRounds},
		{"summary_max_tokens", &cfg.SummaryMaxTokens, defaultSummaryMaxTokens},
		{"rollup_max_tokens", &cfg.RollupMaxTokens, defaultRollupMaxTokens},
		{"summary_max_bytes", &cfg.SummaryMaxBytes, defaultSummaryMaxBytes},
		{"max_summary_passes", &cfg.MaxSummaryPasses, defaultMaxSummaryPasses},
	} {
		if *field.value < 0 {
			return normalizedConfig{}, fmt.Errorf("%s must be positive: %w", field.name, ErrInvalidConfig)
		}
		if *field.value == 0 {
			*field.value = field.fallback
		}
	}
	if cfg.TargetInputTokens >= cfg.TriggerInputTokens || cfg.MemoryTargetTokens >= cfg.MemoryTriggerTokens || cfg.StateTargetTokens >= cfg.StateTriggerTokens {
		return normalizedConfig{}, fmt.Errorf("each token target must be less than its trigger: %w", ErrInvalidConfig)
	}
	accuracies := cfg.AllowedAccuracies
	if accuracies == nil {
		accuracies = []usage.Accuracy{usage.AccuracyExact, usage.AccuracyUpperBound, usage.AccuracyEstimate}
	}
	var mask uint8
	for _, accuracy := range accuracies {
		bit := accuracyBit(accuracy)
		if bit == 0 {
			return normalizedConfig{}, fmt.Errorf("unknown accuracy %q: %w", accuracy, ErrInvalidConfig)
		}
		mask |= bit
	}
	if mask == 0 {
		return normalizedConfig{}, fmt.Errorf("allowed_accuracies must not be empty: %w", ErrInvalidConfig)
	}
	return normalizedConfig{
		provider: cfg.Provider, model: cfg.Model,
		triggerInputTokens: cfg.TriggerInputTokens, targetInputTokens: cfg.TargetInputTokens,
		recentRounds: cfg.RecentRounds, summaryChunkTokens: cfg.SummaryChunkTokens, summaryInputTokens: cfg.SummaryInputTokens,
		summaryMaxTokens: cfg.SummaryMaxTokens, rollupMaxTokens: cfg.RollupMaxTokens, summaryMaxBytes: cfg.SummaryMaxBytes,
		memoryTriggerTokens: cfg.MemoryTriggerTokens, memoryTargetTokens: cfg.MemoryTargetTokens,
		stateTriggerTokens: cfg.StateTriggerTokens, stateTargetTokens: cfg.StateTargetTokens,
		maxSummaryPasses: cfg.MaxSummaryPasses, allowedAccuracies: mask,
	}, nil
}

func accuracyBit(value usage.Accuracy) uint8 {
	switch value {
	case usage.AccuracyExact:
		return 1
	case usage.AccuracyUpperBound:
		return 2
	case usage.AccuracyEstimate:
		return 4
	default:
		return 0
	}
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

var _ contextwindow.Compactor = (*compactor)(nil)
