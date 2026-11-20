// Package loggerstandard implements the SDK logging contract with operator
// configurable levels, text or JSON rendering, standard error or rotating file
// destinations, and bounded record size.
package loggerstandard

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/ingot-abi/invocation"
	"github.com/ingot-agent/ingot-abi/state"
	"github.com/ingot-agent/sdk/logging"
	"github.com/ingot-agent/sdk/observation"
	"github.com/ingot-agent/sdk/operation"
)

// ErrInvalidConfig indicates unsupported configuration values or missing
// dependencies.
var ErrInvalidConfig = errors.New("invalid logger.standard config")

// Dependencies contains the runtime invocation mode and the Plugin-scoped
// persistent state location holding configuration and optional log files.
type Dependencies struct {
	Invocation invocation.Invocation
	State      state.Scope
}

// Exports provides the configured Logger and the Operation that updates its
// live policy.
type Exports struct {
	Logger     logging.Logger
	Operations []operation.Operation
}

// New loads this Plugin's configuration and opens its destination. A missing
// configuration file is the normal Unconfigured state and selects defaults.
// In ModeCheck it validates configuration only and exports a silent Logger so
// graph construction observes the declared capability without opening files.
func New(ctx context.Context, deps Dependencies) (Exports, ingotabi.Cleanup, error) {
	if ctx == nil {
		return Exports{}, nil, fmt.Errorf("construct logger.standard: %w", ErrInvalidConfig)
	}
	if err := ctx.Err(); err != nil {
		return Exports{}, nil, err
	}
	if isNil(deps.State) || deps.State.Dir() == "" {
		return Exports{}, nil, fmt.Errorf("state scope is required: %w", ErrInvalidConfig)
	}
	if isNil(deps.Invocation) {
		return Exports{}, nil, fmt.Errorf("invocation is required: %w", ErrInvalidConfig)
	}
	mode := deps.Invocation.Mode()
	if mode != invocation.ModeRun && mode != invocation.ModeCheck {
		return Exports{}, nil, fmt.Errorf("invalid invocation mode: %w", ErrInvalidConfig)
	}
	stored, err := loadConfig(deps.State.Dir())
	if err != nil {
		return Exports{}, nil, fmt.Errorf("construct logger.standard: %w: %w", err, ErrInvalidConfig)
	}
	config, err := normalizeConfig(stored)
	if err != nil {
		return Exports{}, nil, fmt.Errorf("construct logger.standard: %w", err)
	}
	if mode == invocation.ModeCheck {
		return Exports{Logger: logging.Discard()}, nil, nil
	}
	instance, err := newSink(deps.State.Dir(), config)
	if err != nil {
		return Exports{}, nil, fmt.Errorf("construct logger.standard: %w: %w", err, ErrInvalidConfig)
	}
	cleanup := ingotabi.Cleanup(func(context.Context) error { return instance.Close() })
	return Exports{
		Logger:     &logger{sink: instance},
		Operations: []operation.Operation{&setupOperation{scope: deps.State, sink: instance}},
	}, cleanup, nil
}

// logger adds retained attributes to every record and delegates all policy to
// the shared sink.
type logger struct {
	sink  *sink
	attrs []logging.Attr
}

var _ logging.Logger = (*logger)(nil)

func (l *logger) Enabled(level logging.Level) bool { return l.sink.Enabled(level) }

func (l *logger) Debug(ctx context.Context, message string, attrs ...logging.Attr) {
	l.emit(ctx, logging.LevelDebug, message, attrs)
}

func (l *logger) Info(ctx context.Context, message string, attrs ...logging.Attr) {
	l.emit(ctx, logging.LevelInfo, message, attrs)
}

func (l *logger) Warn(ctx context.Context, message string, attrs ...logging.Attr) {
	l.emit(ctx, logging.LevelWarn, message, attrs)
}

func (l *logger) Error(ctx context.Context, message string, attrs ...logging.Attr) {
	l.emit(ctx, logging.LevelError, message, attrs)
}

func (l *logger) With(attrs ...logging.Attr) logging.Logger {
	retained := make([]logging.Attr, 0, len(l.attrs)+len(attrs))
	retained = append(retained, l.attrs...)
	retained = append(retained, attrs...)
	return &logger{sink: l.sink, attrs: retained}
}

// emit materializes one record. Retained attributes, execution correlation and
// call attributes are combined in that order; mergeAttributes resolves repeats
// so the innermost call wins.
func (l *logger) emit(ctx context.Context, level logging.Level, message string, attrs []logging.Attr) {
	if !l.sink.Enabled(level) {
		return
	}
	record := make([]logging.Attr, 0, len(l.attrs)+len(attrs)+4)
	record = append(record, l.attrs...)
	record = appendCorrelation(record, ctx)
	record = append(record, attrs...)
	l.sink.write(level, message, record)
}

// appendCorrelation adds the execution identity carried by ctx. Correlation is
// optional metadata: a missing or unrelated context adds nothing and never
// drops the record.
func appendCorrelation(attrs []logging.Attr, ctx context.Context) []logging.Attr {
	correlation, ok := observation.CorrelationFromContext(ctx)
	if !ok {
		return attrs
	}
	if correlation.SessionID != "" {
		attrs = append(attrs, logging.String("session_id", string(correlation.SessionID)))
	}
	if correlation.TurnID != "" {
		attrs = append(attrs, logging.String("turn_id", string(correlation.TurnID)))
		attrs = append(attrs, logging.Int("round", int64(correlation.RoundIndex)))
	}
	if correlation.ToolCallID != "" {
		attrs = append(attrs, logging.String("tool_call_id", correlation.ToolCallID))
	}
	return attrs
}

// parseLevel converts a normalized configuration name into a Level.
func parseLevel(name string) (logging.Level, error) {
	level, err := logging.ParseLevel(name)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	return level, nil
}

// levelOf returns the Level for an already validated configuration name.
func levelOf(name string) logging.Level {
	level, err := parseLevel(name)
	if err != nil {
		return logging.LevelInfo
	}
	return level
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
