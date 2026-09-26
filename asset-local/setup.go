package assetlocal

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

var ErrConfigConflict = errors.New("asset.local configuration changed during interaction")

const (
	setupOperationName  = "config"
	setupOperationGroup = "asset-local"
)

// setupOperation asks the Host for this Plugin's configuration through a
// structured interaction request and persists the answer in its own state
// scope. The Host never decodes plugin configuration.
type setupOperation struct {
	scope state.Scope
	store *store
}

var _ operation.Operation = (*setupOperation)(nil)

func (*setupOperation) Definition() operation.Definition {
	return operation.Definition{
		Name:        setupOperationName,
		Description: "Review and update the asset.local storage limits.",
		Group:       setupOperationGroup,
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`),
		OutputSchema: json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["max_object_bytes", "max_total_bytes", "io_concurrency", "restart_required"],
  "properties": {
    "max_object_bytes": {"type": "integer"},
    "max_total_bytes": {"type": "integer"},
    "io_concurrency": {"type": "integer"},
    "restart_required": {"type": "boolean"}
  }
}`),
	}
}

func (o *setupOperation) Invoke(ctx context.Context, request operation.Request) (operation.Result, error) {
	if err := ctx.Err(); err != nil {
		return operation.Result{}, err
	}
	if o.scope == nil || o.scope.Dir() == "" {
		return operation.Result{}, fmt.Errorf("asset.local config: %w", ErrInvalidConfig)
	}
	current, err := loadConfig(o.scope.Dir())
	if err != nil {
		return operation.Result{}, err
	}
	objectDefault := current.MaxObjectBytes
	if objectDefault == 0 {
		objectDefault = defaultMaxObjectBytes
	}
	totalDefault := current.MaxTotalBytes
	if totalDefault == 0 {
		totalDefault = defaultMaxTotalBytes
	}
	concurrencyDefault := int64(current.IOConcurrency)
	if concurrencyDefault == 0 {
		concurrencyDefault = defaultIOConcurrency
	}
	response, err := request.Interaction.Request(ctx, interaction.Request{
		Name:        setupOperationName,
		Description: "Immutable asset storage bounds and concurrent I/O.",
		Fields: []interaction.Field{
			{Name: "max_object_bytes", Label: "Max object bytes", Description: "Largest single stored asset.", Kind: interaction.FieldInteger, Required: true, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: objectDefault}},
			{Name: "max_total_bytes", Label: "Max total bytes", Description: "Total local asset capacity.", Kind: interaction.FieldInteger, Required: true, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: totalDefault}},
			{Name: "io_concurrency", Label: "I/O concurrency", Description: "Concurrent asset I/O slots.", Kind: interaction.FieldInteger, Required: true, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: concurrencyDefault}},
		},
	})
	if err != nil {
		return operation.Result{}, err
	}
	object, ok := answerInteger(response, "max_object_bytes")
	if !ok {
		return operation.Result{}, fmt.Errorf("asset.local config: missing max_object_bytes: %w", ErrInvalidConfig)
	}
	total, ok := answerInteger(response, "max_total_bytes")
	if !ok {
		return operation.Result{}, fmt.Errorf("asset.local config: missing max_total_bytes: %w", ErrInvalidConfig)
	}
	concurrency, ok := answerInteger(response, "io_concurrency")
	if !ok {
		return operation.Result{}, fmt.Errorf("asset.local config: missing io_concurrency: %w", ErrInvalidConfig)
	}
	updated := Config{MaxObjectBytes: object, MaxTotalBytes: total, IOConcurrency: int(concurrency)}
	effective, err := normalizeConfig(updated)
	if err != nil {
		return operation.Result{}, err
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
	o.store.configMu.Lock()
	if err := ctx.Err(); err != nil {
		o.store.configMu.Unlock()
		return operation.Result{}, err
	}
	o.store.mu.Lock()
	totalBytes := o.store.total
	o.store.mu.Unlock()
	if totalBytes > uint64(effective.MaxTotalBytes) {
		o.store.configMu.Unlock()
		return operation.Result{}, fmt.Errorf("stored assets exceed configured capacity: %w", ErrCapacity)
	}
	if err := saveConfig(o.scope.Dir(), updated); err != nil {
		o.store.configMu.Unlock()
		return operation.Result{}, err
	}
	o.store.maxObject = uint64(effective.MaxObjectBytes)
	o.store.maxTotal = uint64(effective.MaxTotalBytes)
	o.store.limiter.setLimit(effective.IOConcurrency)
	o.store.configMu.Unlock()
	output, err := json.Marshal(map[string]any{
		"max_object_bytes": updated.MaxObjectBytes,
		"max_total_bytes":  updated.MaxTotalBytes,
		"io_concurrency":   updated.IOConcurrency,
		"restart_required": false,
	})
	if err != nil {
		return operation.Result{}, err
	}
	return operation.Result{Output: output}, nil
}

func answerInteger(response interaction.Response, name string) (int64, bool) {
	for _, answer := range response.Values {
		if answer.Name != name {
			continue
		}
		if answer.Value.Kind != interaction.ValueInteger {
			return 0, false
		}
		return answer.Value.Integer, true
	}
	return 0, false
}
