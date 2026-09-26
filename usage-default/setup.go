package usagedefault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/ingot-agent/ingot-abi/state"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/operation"
)

var ErrConfigConflict = errors.New("usage.default configuration changed during interaction")

const (
	setupOperationName  = "config"
	setupOperationGroup = "usage-default"
)

// setupOperation persists the cache limit in this Plugin's state scope.
type setupOperation struct {
	scope   state.Scope
	counter *counter
}

var _ operation.Operation = (*setupOperation)(nil)

func (*setupOperation) Definition() operation.Definition {
	return operation.Definition{
		Name:         setupOperationName,
		Description:  "Review and update the usage.default cache size.",
		Group:        setupOperationGroup,
		InputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`),
		OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["restart_required"],"properties":{"restart_required":{"type":"boolean"}}}`),
	}
}

func (o *setupOperation) Invoke(ctx context.Context, request operation.Request) (operation.Result, error) {
	if err := ctx.Err(); err != nil {
		return operation.Result{}, err
	}
	if o.scope == nil || o.scope.Dir() == "" {
		return operation.Result{}, fmt.Errorf("usage.default config: state scope is required: %w", ErrInvalidConfig)
	}
	current, err := loadConfig(o.scope.Dir())
	if err != nil {
		return operation.Result{}, err
	}
	response, err := request.Interaction.Request(ctx, interaction.Request{
		Name:        setupOperationName,
		Description: "Token estimate cache size.",
		Fields: []interaction.Field{
			{Name: "cache_entries", Label: "Cache entries", Description: "Zero selects the built-in default.", Kind: interaction.FieldInteger, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: int64(current.CacheEntries)}},
		},
	})
	if err != nil {
		return operation.Result{}, err
	}
	updated := Config{CacheEntries: current.CacheEntries}
	if v, ok := answerInteger(response, "cache_entries"); ok {
		updated.CacheEntries = int(v)
	}
	if updated.CacheEntries < 0 {
		return operation.Result{}, fmt.Errorf("cache_entries must not be negative: %w", ErrInvalidConfig)
	}
	effective := updated
	if effective.CacheEntries == 0 {
		effective.CacheEntries = defaultCacheEntries
	}
	if err := ctx.Err(); err != nil {
		return operation.Result{}, err
	}
	configCommitMu.Lock()
	defer configCommitMu.Unlock()
	latest, err := loadConfig(o.scope.Dir())
	if err != nil {
		return operation.Result{}, err
	}
	if !reflect.DeepEqual(latest, current) {
		return operation.Result{}, ErrConfigConflict
	}
	if err := ctx.Err(); err != nil {
		return operation.Result{}, err
	}
	if err := saveConfig(o.scope.Dir(), updated); err != nil {
		return operation.Result{}, err
	}
	o.counter.applyConfig(effective.CacheEntries)
	return operation.Result{Output: json.RawMessage(`{"restart_required":false}`)}, nil
}

func answerInteger(response interaction.Response, name string) (int64, bool) {
	for _, answer := range response.Values {
		if answer.Name == name && answer.Value.Kind == interaction.ValueInteger {
			return answer.Value.Integer, true
		}
	}
	return 0, false
}
