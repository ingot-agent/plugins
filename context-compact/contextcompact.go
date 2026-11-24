// Package contextcompact keeps a model invocation inside a token budget without
// ever failing because compaction could not reach its target.
//
// Compaction runs in two stages. The first is deterministic: rounds are elided
// from tool output down to narrative, and only then removed. It cannot fail and
// needs no model call. The second stage summarizes what the first stage removed.
// It is best effort: a summarizer that is slow, broken, or unavailable costs
// recall, never the turn.
package contextcompact

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/ingot-abi/state"
	"github.com/ingot-agent/sdk/contextwindow"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/logging"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/operation"
	"github.com/ingot-agent/sdk/session"
)

const (
	defaultTriggerTokens       = 800_000
	defaultTargetTokens        = 250_000
	defaultProtectRecentTokens = 100_000
	defaultSummaryMaxTokens    = 2_048
	defaultSummaryMaxBytes     = 64 * 1024
)

var (
	// ErrInvalidConfig reports unusable plugin configuration.
	ErrInvalidConfig = errors.New("invalid context.compact config")
	// ErrInvalidRequest reports a malformed compaction request.
	ErrInvalidRequest = errors.New("invalid context compaction request")
)

// Dependencies are the capabilities this Plugin needs. The model runtime is
// used only for summarization, which is optional at call time: without a usable
// summarizer the Plugin still compacts by eliding and dropping.
type Dependencies struct {
	Model           model.Runtime
	ProviderSources []model.ProviderSource
	Interactions    ingotabi.Optional[interaction.ExecutionBinder]
	Logger          ingotabi.Optional[logging.Logger]
	Store           session.Store
	State           state.Scope
}

// Exports are the capabilities this Plugin provides.
type Exports struct {
	Compactor  contextwindow.Compactor
	Operations []operation.Operation
}

type compactor struct {
	model        model.Runtime
	interactions interaction.ExecutionBinder
	logger       logging.Logger
	store        session.Store
	loader       *configLoader
	gates        *gateManager
}

// New constructs the compaction capability.
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
	if deps.Interactions.Valid && isNil(deps.Interactions.Value) {
		return Exports{}, nil, fmt.Errorf("interaction binder is nil: %w", ErrInvalidConfig)
	}
	logger := logging.Discard()
	if deps.Logger.Valid {
		if isNil(deps.Logger.Value) {
			return Exports{}, nil, fmt.Errorf("logger is typed nil: %w", ErrInvalidConfig)
		}
		logger = deps.Logger.Value
	}
	for i, source := range deps.ProviderSources {
		if isNil(source) {
			return Exports{}, nil, fmt.Errorf("provider_sources[%d] is nil: %w", i, ErrInvalidConfig)
		}
	}
	loader, err := newConfigLoader(deps.State.Dir(), logger)
	if err != nil {
		return Exports{}, nil, err
	}
	instance := &compactor{
		model:  deps.Model,
		logger: logger,
		store:  deps.Store,
		loader: loader,
		gates:  newGateManager(),
	}
	if deps.Interactions.Valid {
		instance.interactions = deps.Interactions.Value
	}
	cleanup := ingotabi.Cleanup(func(context.Context) error { return nil })
	return Exports{
		Compactor:  instance,
		Operations: []operation.Operation{newSetupOperation(deps.State, deps.ProviderSources, instance, loader)},
	}, cleanup, nil
}

// isNil reports whether a dependency is absent, including a typed nil.
func isNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Ptr, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan, reflect.Interface:
		return reflected.IsNil()
	default:
		return false
	}
}
