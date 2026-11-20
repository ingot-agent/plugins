package loggerstandard

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

// ErrConfigConflict indicates that another writer changed the configuration
// while this Operation was collecting an answer.
var ErrConfigConflict = errors.New("logger.standard configuration changed during interaction")

const (
	setupOperationName  = "config"
	setupOperationGroup = "logger-standard"
)

// setupOperation asks the Host for this Plugin's logging policy through a
// structured interaction request, persists the answer in its own state scope
// and applies it to the running Logger. The Host never decodes the file.
type setupOperation struct {
	scope state.Scope
	sink  *sink
}

var _ operation.Operation = (*setupOperation)(nil)

func (*setupOperation) Definition() operation.Definition {
	return operation.Definition{
		Name:        setupOperationName,
		Description: "Review and update log level, rendering, destination and file retention.",
		Group:       setupOperationGroup,
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`),
		OutputSchema: json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["level", "format", "output", "path", "max_record_bytes", "max_size_bytes", "max_backups", "restart_required"],
  "properties": {
    "level": {"type": "string"},
    "format": {"type": "string"},
    "output": {"type": "string"},
    "path": {"type": "string"},
    "max_record_bytes": {"type": "integer"},
    "max_size_bytes": {"type": "integer"},
    "max_backups": {"type": "integer"},
    "restart_required": {"type": "boolean"}
  }
}`),
	}
}

func (o *setupOperation) Invoke(ctx context.Context, request operation.Request) (operation.Result, error) {
	if err := ctx.Err(); err != nil {
		return operation.Result{}, err
	}
	if o.scope == nil || o.scope.Dir() == "" || o.sink == nil {
		return operation.Result{}, fmt.Errorf("logger.standard config: %w", ErrInvalidConfig)
	}
	current, err := loadConfig(o.scope.Dir())
	if err != nil {
		return operation.Result{}, err
	}
	effective, err := normalizeConfig(current)
	if err != nil {
		return operation.Result{}, err
	}
	response, err := request.Interaction.Request(ctx, interaction.Request{
		Name:        setupOperationName,
		Description: "Log records below the selected level are discarded. File retention applies only when the destination is a file.",
		Fields: []interaction.Field{
			{Name: "level", Label: "Minimum level", Description: "Lowest severity that is emitted.", Kind: interaction.FieldChoice, Required: true,
				Default: &interaction.Value{Kind: interaction.ValueString, String: effective.Level},
				Options: []interaction.Option{
					{Value: "debug", Label: "Debug", Description: "Detailed diagnostics."},
					{Value: "info", Label: "Info", Description: "Routine progress."},
					{Value: "warn", Label: "Warn", Description: "Recoverable problems."},
					{Value: "error", Label: "Error", Description: "Failures only."},
				}},
			{Name: "format", Label: "Format", Description: "Record rendering.", Kind: interaction.FieldChoice, Required: true,
				Default: &interaction.Value{Kind: interaction.ValueString, String: effective.Format},
				Options: []interaction.Option{
					{Value: "text", Label: "Text", Description: "Human-readable key=value lines."},
					{Value: "json", Label: "JSON", Description: "One JSON object per line."},
				}},
			{Name: "output", Label: "Destination", Description: "Where records are written.", Kind: interaction.FieldChoice, Required: true,
				Default: &interaction.Value{Kind: interaction.ValueString, String: effective.Output},
				Options: []interaction.Option{
					{Value: "stderr", Label: "Standard error", Description: "The Runtime Image's own error stream."},
					{Value: "file", Label: "File", Description: "A rotating file inside this Plugin's state scope."},
				}},
			{Name: "path", Label: "File path", Description: "Absolute, or relative to this Plugin's state directory.", Kind: interaction.FieldString, Required: true,
				Default: &interaction.Value{Kind: interaction.ValueString, String: effective.Path},
				Options: []interaction.Option{{Value: defaultPath, Label: "Default", Description: "logs/ingot.log under the state scope."}}},
			{Name: "max_record_bytes", Label: "Max record bytes", Description: "Bounds one attribute value and one complete record.", Kind: interaction.FieldInteger, Required: true,
				Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: int64(effective.MaxRecordBytes)}},
			{Name: "max_size_bytes", Label: "Max file bytes", Description: "Rotate the file destination at this size.", Kind: interaction.FieldInteger, Required: true,
				Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: effective.MaxSizeBytes}},
			{Name: "max_backups", Label: "Retained files", Description: "Number of rotated files kept.", Kind: interaction.FieldInteger, Required: true,
				Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: int64(effective.MaxBackups)}},
		},
	})
	if err != nil {
		return operation.Result{}, err
	}
	updated, err := answerConfig(response, effective)
	if err != nil {
		return operation.Result{}, err
	}
	normalized, err := normalizeConfig(updated)
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
	if err := o.sink.Apply(normalized); err != nil {
		return operation.Result{}, err
	}
	if err := saveConfig(o.scope.Dir(), normalized); err != nil {
		if rollbackErr := o.sink.Apply(effective); rollbackErr != nil {
			return operation.Result{}, fmt.Errorf("save plugin config: %w (rollback failed: %v)", err, rollbackErr)
		}
		return operation.Result{}, err
	}
	output, err := json.Marshal(map[string]any{
		"level":            normalized.Level,
		"format":           normalized.Format,
		"output":           normalized.Output,
		"path":             normalized.Path,
		"max_record_bytes": normalized.MaxRecordBytes,
		"max_size_bytes":   normalized.MaxSizeBytes,
		"max_backups":      normalized.MaxBackups,
		"restart_required": false,
	})
	if err != nil {
		return operation.Result{}, err
	}
	return operation.Result{Output: output}, nil
}

// answerConfig reads the complete answer set. A missing or wrongly typed answer
// is rejected instead of silently keeping a previous value.
func answerConfig(response interaction.Response, fallback Config) (Config, error) {
	config := fallback
	level, err := answerString(response, "level")
	if err != nil {
		return Config{}, err
	}
	format, err := answerString(response, "format")
	if err != nil {
		return Config{}, err
	}
	output, err := answerString(response, "output")
	if err != nil {
		return Config{}, err
	}
	path, err := answerString(response, "path")
	if err != nil {
		return Config{}, err
	}
	maxRecord, err := answerInteger(response, "max_record_bytes")
	if err != nil {
		return Config{}, err
	}
	maxSize, err := answerInteger(response, "max_size_bytes")
	if err != nil {
		return Config{}, err
	}
	maxBackups, err := answerInteger(response, "max_backups")
	if err != nil {
		return Config{}, err
	}
	config.Level, config.Format, config.Output, config.Path = level, format, output, path
	config.MaxRecordBytes = int(maxRecord)
	config.MaxSizeBytes = maxSize
	config.MaxBackups = int(maxBackups)
	return config, nil
}

func answerString(response interaction.Response, name string) (string, error) {
	for _, answer := range response.Values {
		if answer.Name != name {
			continue
		}
		if answer.Value.Kind != interaction.ValueString {
			return "", fmt.Errorf("answer %q is not a string: %w", name, ErrInvalidConfig)
		}
		return answer.Value.String, nil
	}
	return "", fmt.Errorf("missing answer %q: %w", name, ErrInvalidConfig)
}

func answerInteger(response interaction.Response, name string) (int64, error) {
	for _, answer := range response.Values {
		if answer.Name != name {
			continue
		}
		if answer.Value.Kind != interaction.ValueInteger {
			return 0, fmt.Errorf("answer %q is not an integer: %w", name, ErrInvalidConfig)
		}
		return answer.Value.Integer, nil
	}
	return 0, fmt.Errorf("missing answer %q: %w", name, ErrInvalidConfig)
}
