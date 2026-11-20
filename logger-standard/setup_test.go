package loggerstandard

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ingot-agent/ingot-abi/invocation"
	"github.com/ingot-agent/ingot-abi/state"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/logging"
	"github.com/ingot-agent/sdk/operation"
)

type fixedScope string

func (s fixedScope) Dir() string { return string(s) }

type fixedInvocation struct{ mode invocation.Mode }

func (f fixedInvocation) Arguments() []string   { return nil }
func (f fixedInvocation) Mode() invocation.Mode { return f.mode }

// answerChannel returns a fixed answer set and optionally runs a hook while the
// Host is deciding, which models a concurrent writer.
type answerChannel struct {
	response  interaction.Response
	err       error
	requests  []interaction.Request
	onRequest func()
}

func (c *answerChannel) Request(ctx context.Context, request interaction.Request) (interaction.Response, error) {
	if err := ctx.Err(); err != nil {
		return interaction.Response{}, err
	}
	c.requests = append(c.requests, request)
	if c.onRequest != nil {
		c.onRequest()
	}
	return c.response, c.err
}

func (*answerChannel) Emit(context.Context, interaction.Event) error { return nil }
func (*answerChannel) Set(context.Context, interaction.State) error  { return nil }
func (*answerChannel) Clear(context.Context, string) error           { return nil }

func completeAnswers(level, format, output, path string, maxRecord, maxSize, maxBackups int64) interaction.Response {
	return interaction.Response{Values: []interaction.Answer{
		{Name: "level", Value: interaction.StringValue(level)},
		{Name: "format", Value: interaction.StringValue(format)},
		{Name: "output", Value: interaction.StringValue(output)},
		{Name: "path", Value: interaction.StringValue(path)},
		{Name: "max_record_bytes", Value: interaction.IntegerValue(maxRecord)},
		{Name: "max_size_bytes", Value: interaction.IntegerValue(maxSize)},
		{Name: "max_backups", Value: interaction.IntegerValue(maxBackups)},
	}}
}

func TestNewRequiresDependencies(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tests := []struct {
		name string
		ctx  context.Context
		deps Dependencies
	}{
		{name: "nil context", ctx: nil, deps: Dependencies{Invocation: fixedInvocation{mode: invocation.ModeRun}, State: fixedScope(dir)}},
		{name: "nil state", ctx: context.Background(), deps: Dependencies{Invocation: fixedInvocation{mode: invocation.ModeRun}}},
		{name: "nil invocation", ctx: context.Background(), deps: Dependencies{State: fixedScope(dir)}},
		{name: "invalid mode", ctx: context.Background(), deps: Dependencies{Invocation: fixedInvocation{mode: invocation.Mode(9)}, State: fixedScope(dir)}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, _, err := New(test.ctx, test.deps); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("New error = %v, want ErrInvalidConfig", err)
			}
		})
	}
}

func TestNewExportsWorkingLogger(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	exports, cleanup, err := New(context.Background(), Dependencies{
		Invocation: fixedInvocation{mode: invocation.ModeRun},
		State:      fixedScope(dir),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if cleanup == nil {
		t.Fatal("New returned a nil cleanup")
	}
	defer func() {
		if err := cleanup(context.Background()); err != nil {
			t.Fatalf("cleanup: %v", err)
		}
	}()
	if exports.Logger == nil {
		t.Fatal("New returned a nil Logger")
	}
	if len(exports.Operations) != 1 {
		t.Fatalf("operations = %d, want 1", len(exports.Operations))
	}
	exports.Logger.Warn(context.Background(), "visible")
	exports.Logger.Debug(context.Background(), "hidden")
}

func TestNewCheckModeDoesNotOpenDestination(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := saveConfig(dir, Config{Output: "file", Path: "logs/check.log"}); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	exports, cleanup, err := New(context.Background(), Dependencies{
		Invocation: fixedInvocation{mode: invocation.ModeCheck},
		State:      fixedScope(dir),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if cleanup != nil {
		t.Fatal("check mode returned a cleanup for a destination it must not open")
	}
	if exports.Logger.Enabled(logging.LevelError) {
		t.Fatal("check mode exported an enabled Logger")
	}
	if _, err := os.Stat(filepath.Join(dir, "logs")); !os.IsNotExist(err) {
		t.Fatalf("check mode created the log directory")
	}
}

func TestSetupOperationAppliesAndPersists(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	instance, err := newSink(dir, Config{})
	if err != nil {
		t.Fatalf("newSink: %v", err)
	}
	defer func() { _ = instance.Close() }()
	setup := &setupOperation{scope: fixedScope(dir), sink: instance}
	channel := &answerChannel{response: completeAnswers("warn", "json", "file", "logs/op.log", 4096, 8192, 2)}
	result, err := setup.Invoke(context.Background(), operation.Request{Interaction: channel})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if len(channel.requests) != 1 {
		t.Fatalf("interaction requests = %d, want 1", len(channel.requests))
	}
	var output map[string]any
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if output["level"] != "warn" || output["format"] != "json" || output["output"] != "file" {
		t.Fatalf("output = %v", output)
	}
	if output["restart_required"] != false {
		t.Fatalf("restart_required = %v", output["restart_required"])
	}
	stored, err := loadConfig(dir)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if stored.Level != "warn" || stored.Path != "logs/op.log" {
		t.Fatalf("stored config = %+v", stored)
	}
	(&logger{sink: instance}).Info(context.Background(), "discarded")
	(&logger{sink: instance}).Error(context.Background(), "retained")
	data, err := os.ReadFile(filepath.Join(dir, "logs", "op.log"))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if string(data) == "" || strings.Contains(string(data), "discarded") {
		t.Fatalf("log = %q, want only the retained record", string(data))
	}
	if !strings.Contains(string(data), `"level":"ERROR"`) {
		t.Fatalf("log = %q, want JSON rendering", string(data))
	}
}

func TestSetupOperationRejectsConcurrentWriter(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	instance, err := newSink(dir, Config{})
	if err != nil {
		t.Fatalf("newSink: %v", err)
	}
	defer func() { _ = instance.Close() }()
	setup := &setupOperation{scope: fixedScope(dir), sink: instance}
	channel := &answerChannel{
		response: completeAnswers("debug", "text", "stderr", "logs/app.log", 1024, 1024, 1),
		onRequest: func() {
			if err := saveConfig(dir, Config{Level: "error"}); err != nil {
				t.Errorf("concurrent save: %v", err)
			}
		},
	}
	if _, err := setup.Invoke(context.Background(), operation.Request{Interaction: channel}); !errors.Is(err, ErrConfigConflict) {
		t.Fatalf("Invoke error = %v, want ErrConfigConflict", err)
	}
}

func TestSetupOperationRejectsIncompleteAnswer(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	instance, err := newSink(dir, Config{})
	if err != nil {
		t.Fatalf("newSink: %v", err)
	}
	defer func() { _ = instance.Close() }()
	setup := &setupOperation{scope: fixedScope(dir), sink: instance}
	channel := &answerChannel{response: interaction.Response{Values: []interaction.Answer{
		{Name: "level", Value: interaction.StringValue("info")},
	}}}
	if _, err := setup.Invoke(context.Background(), operation.Request{Interaction: channel}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("Invoke error = %v, want ErrInvalidConfig", err)
	}
}

func TestSetupOperationRejectsInvalidAnswerValues(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	instance, err := newSink(dir, Config{})
	if err != nil {
		t.Fatalf("newSink: %v", err)
	}
	defer func() { _ = instance.Close() }()
	setup := &setupOperation{scope: fixedScope(dir), sink: instance}
	channel := &answerChannel{response: completeAnswers("trace", "text", "stderr", "logs/app.log", 1024, 1024, 1)}
	if _, err := setup.Invoke(context.Background(), operation.Request{Interaction: channel}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("Invoke error = %v, want ErrInvalidConfig", err)
	}
}

func TestSetupOperationReportsMissingScope(t *testing.T) {
	t.Parallel()
	setup := &setupOperation{}
	if _, err := setup.Invoke(context.Background(), operation.Request{Interaction: &answerChannel{}}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("Invoke error = %v, want ErrInvalidConfig", err)
	}
}

var _ state.Scope = fixedScope("")
