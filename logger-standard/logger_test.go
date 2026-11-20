package loggerstandard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ingot-agent/sdk/logging"
	"github.com/ingot-agent/sdk/observation"
	"github.com/ingot-agent/sdk/session"
)

// captureSink replaces the configured destination with an in-memory buffer so
// a test can assert exact rendering without touching the filesystem.
func captureSink(t *testing.T, cfg Config) (*sink, *bytes.Buffer) {
	t.Helper()
	instance, err := newSink(t.TempDir(), cfg)
	if err != nil {
		t.Fatalf("newSink: %v", err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	buffer := &bytes.Buffer{}
	instance.mu.Lock()
	if instance.file != nil {
		_ = instance.file.Close()
		instance.file = nil
	}
	instance.out = buffer
	instance.size = 0
	instance.mu.Unlock()
	return instance, buffer
}

func TestLevelFilteringDropsLowerSeverity(t *testing.T) {
	t.Parallel()
	instance, buffer := captureSink(t, Config{Level: "warn", Format: "text"})
	logger := &logger{sink: instance}
	ctx := context.Background()
	logger.Debug(ctx, "debug-record")
	logger.Info(ctx, "info-record")
	logger.Warn(ctx, "warn-record")
	logger.Error(ctx, "error-record")
	lines := nonEmptyLines(buffer.String())
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2: %q", len(lines), buffer.String())
	}
	if !strings.Contains(lines[0], "msg=warn-record") || !strings.Contains(lines[0], "level=WARN") {
		t.Fatalf("first line = %q", lines[0])
	}
	if !strings.Contains(lines[1], "msg=error-record") || !strings.Contains(lines[1], "level=ERROR") {
		t.Fatalf("second line = %q", lines[1])
	}
}

func TestEnabledTracksMinimumLevel(t *testing.T) {
	t.Parallel()
	instance, _ := captureSink(t, Config{Level: "info"})
	logger := &logger{sink: instance}
	if logger.Enabled(logging.LevelDebug) {
		t.Fatal("Enabled(debug) = true")
	}
	if !logger.Enabled(logging.LevelInfo) || !logger.Enabled(logging.LevelError) {
		t.Fatal("Enabled rejected levels at or above the minimum")
	}
}

func TestTextRendering(t *testing.T) {
	t.Parallel()
	instance, buffer := captureSink(t, Config{Level: "debug", Format: "text"})
	logger := &logger{sink: instance}
	logger.Info(context.Background(), "hello world",
		logging.String("key", "value"),
		logging.Int("count", 3),
		logging.Bool("ok", true),
		logging.Group("nested", logging.String("inner", "deep")),
		logging.Err(fmt.Errorf("boom")),
	)
	line := strings.TrimSpace(buffer.String())
	for _, want := range []string{
		"level=INFO", `msg="hello world"`, "key=value", "count=3", "ok=true",
		"nested.inner=deep", `error=boom`,
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("line %q is missing %q", line, want)
		}
	}
	if strings.Count(line, "\n") != 0 {
		t.Fatalf("record contains an embedded newline: %q", line)
	}
}

func TestJSONRendering(t *testing.T) {
	t.Parallel()
	instance, buffer := captureSink(t, Config{Level: "debug", Format: "json"})
	logger := &logger{sink: instance}
	logger.Warn(context.Background(), "warning", logging.Int("attempt", 2),
		logging.Group("provider", logging.String("name", "example")))
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buffer.Bytes()), &record); err != nil {
		t.Fatalf("decode record %q: %v", buffer.String(), err)
	}
	if record["level"] != "WARN" || record["msg"] != "warning" {
		t.Fatalf("record = %v", record)
	}
	if record["attempt"] != float64(2) {
		t.Fatalf("attempt = %v", record["attempt"])
	}
	group, ok := record["provider"].(map[string]any)
	if !ok || group["name"] != "example" {
		t.Fatalf("provider = %v", record["provider"])
	}
	if _, ok := record["time"].(string); !ok {
		t.Fatalf("time = %v", record["time"])
	}
}

func TestWithRetainsAndCallOverridesAttributes(t *testing.T) {
	t.Parallel()
	instance, buffer := captureSink(t, Config{Level: "debug", Format: "json"})
	logger := (&logger{sink: instance}).With(logging.String("component", "test"), logging.String("key", "retained"))
	logger.Info(context.Background(), "message", logging.String("key", "explicit"))
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buffer.Bytes()), &record); err != nil {
		t.Fatalf("decode record: %v", err)
	}
	if record["component"] != "test" {
		t.Fatalf("component = %v", record["component"])
	}
	if record["key"] != "explicit" {
		t.Fatalf("key = %v, want the call attribute to win", record["key"])
	}
	if strings.Count(buffer.String(), `"key"`) != 1 {
		t.Fatalf("duplicate key in record: %q", buffer.String())
	}
}

func TestCorrelationFromContext(t *testing.T) {
	t.Parallel()
	instance, buffer := captureSink(t, Config{Level: "debug", Format: "json"})
	logger := &logger{sink: instance}
	ctx := observation.WithCorrelation(context.Background(), observation.Correlation{
		SessionID:  session.ID("session-1"),
		TurnID:     observation.ID("turn-1"),
		RoundIndex: 2,
		ToolCallID: "call-1",
	})
	logger.Info(ctx, "correlated")
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buffer.Bytes()), &record); err != nil {
		t.Fatalf("decode record: %v", err)
	}
	want := map[string]any{
		"session_id":   "session-1",
		"turn_id":      "turn-1",
		"round":        float64(2),
		"tool_call_id": "call-1",
	}
	for key, value := range want {
		if record[key] != value {
			t.Fatalf("%s = %v, want %v", key, record[key], value)
		}
	}
}

func TestCorrelationWithoutSessionAddsNothing(t *testing.T) {
	t.Parallel()
	instance, buffer := captureSink(t, Config{Level: "debug", Format: "json"})
	(&logger{sink: instance}).Info(context.Background(), "plain")
	for _, key := range []string{"session_id", "turn_id", "round", "tool_call_id"} {
		if strings.Contains(buffer.String(), key) {
			t.Fatalf("record without correlation contains %q: %q", key, buffer.String())
		}
	}
}

func TestOversizedValueIsTruncated(t *testing.T) {
	t.Parallel()
	instance, buffer := captureSink(t, Config{Level: "debug", Format: "json", MaxRecordBytes: 1024})
	logger := &logger{sink: instance}
	logger.Info(context.Background(), "large", logging.String("payload", strings.Repeat("a", 64*1024)))
	if buffer.Len() > 4*1024 {
		t.Fatalf("record length = %d, want bounded", buffer.Len())
	}
	if !strings.Contains(buffer.String(), "truncated") {
		t.Fatalf("record does not report truncation: %q", buffer.String())
	}
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buffer.Bytes()), &record); err != nil {
		t.Fatalf("decoded truncated record is not valid JSON: %v", err)
	}
}

func TestOversizedRecordDropsAttributesThatDoNotFit(t *testing.T) {
	t.Parallel()
	instance, buffer := captureSink(t, Config{Level: "debug", Format: "json", MaxRecordBytes: 512})
	logger := &logger{sink: instance}
	attrs := make([]logging.Attr, 0, 64)
	for index := 0; index < 64; index++ {
		attrs = append(attrs, logging.String(fmt.Sprintf("key_%d", index), strings.Repeat("v", 64)))
	}
	logger.Info(context.Background(), "many", attrs...)
	if buffer.Len() > 4*1024 {
		t.Fatalf("record length = %d, want bounded", buffer.Len())
	}
	if !strings.Contains(buffer.String(), "truncated_attrs") {
		t.Fatalf("record does not report dropped attributes: %q", buffer.String())
	}
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buffer.Bytes()), &record); err != nil {
		t.Fatalf("decoded record is not valid JSON: %v", err)
	}
	if _, ok := record["truncated_attrs"]; !ok {
		t.Fatalf("record = %v", record)
	}
}

func TestJSONEscapesControlCharacters(t *testing.T) {
	t.Parallel()
	instance, buffer := captureSink(t, Config{Level: "debug", Format: "json"})
	logger := &logger{sink: instance}
	message := "bell\a newline\n end"
	payload := "\x01\x02"
	logger.Info(context.Background(), message, logging.String("payload", payload))
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buffer.Bytes()), &record); err != nil {
		t.Fatalf("record is not valid JSON: %v (%q)", err, buffer.String())
	}
	if record["msg"] != message {
		t.Fatalf("msg = %q, want %q", record["msg"], message)
	}
	if record["payload"] != payload {
		t.Fatalf("payload = %q, want %q", record["payload"], payload)
	}
}

func TestEmptyKeyIsIgnored(t *testing.T) {
	t.Parallel()
	instance, buffer := captureSink(t, Config{Level: "debug", Format: "text"})
	(&logger{sink: instance}).Info(context.Background(), "message", logging.Attr{Key: "", Value: logging.StringValue("ignored")})
	line := strings.TrimSpace(buffer.String())
	if strings.Contains(line, "ignored") {
		t.Fatalf("empty key was rendered: %q", line)
	}
}

func TestApplySwitchesLevelAndFormat(t *testing.T) {
	t.Parallel()
	instance, buffer := captureSink(t, Config{Level: "error", Format: "text"})
	logger := &logger{sink: instance}
	logger.Info(context.Background(), "hidden")
	if err := instance.Apply(Config{Level: "debug", Format: "json"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// Apply re-opens the configured destination, so restore the capture buffer.
	instance.mu.Lock()
	instance.out = buffer
	instance.mu.Unlock()
	logger.Info(context.Background(), "visible", logging.String("k", "v"))
	if strings.Contains(buffer.String(), "hidden") {
		t.Fatalf("disabled level was emitted: %q", buffer.String())
	}
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buffer.Bytes()), &record); err != nil {
		t.Fatalf("record was not rendered as JSON after Apply: %q", buffer.String())
	}
	if record["msg"] != "visible" || record["k"] != "v" {
		t.Fatalf("record = %v", record)
	}
}

func TestConcurrentWritesAreCompleteAndOrderedPerRecord(t *testing.T) {
	t.Parallel()
	instance, buffer := captureSink(t, Config{Level: "debug", Format: "text"})
	logger := &logger{sink: instance}
	var wait sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		worker := worker
		wait.Add(1)
		go func() {
			defer wait.Done()
			for index := 0; index < 50; index++ {
				logger.Info(context.Background(), "concurrent",
					logging.Int("worker", int64(worker)), logging.Int("index", int64(index)))
			}
		}()
	}
	wait.Wait()
	lines := nonEmptyLines(buffer.String())
	if len(lines) != 16*50 {
		t.Fatalf("lines = %d, want %d", len(lines), 16*50)
	}
	for _, line := range lines {
		if !strings.Contains(line, "worker=") || !strings.Contains(line, "index=") {
			t.Fatalf("incomplete record: %q", line)
		}
	}
}

func TestNewSinkRejectsInvalidConfig(t *testing.T) {
	t.Parallel()
	for _, cfg := range []Config{
		{Level: "trace"},
		{Format: "yaml"},
		{Output: "stdout"},
		{MaxRecordBytes: -1},
		{MaxSizeBytes: -1},
		{MaxBackups: -1},
	} {
		if _, err := newSink(t.TempDir(), cfg); err == nil {
			t.Fatalf("newSink(%+v) accepted invalid config", cfg)
		}
	}
}

func TestFileDestinationRotatesAndRetainsBackups(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := Config{Level: "debug", Format: "text", Output: "file", Path: "logs/test.log",
		MaxRecordBytes: 4096, MaxSizeBytes: 600, MaxBackups: 2}
	instance, err := newSink(dir, cfg)
	if err != nil {
		t.Fatalf("newSink: %v", err)
	}
	logger := &logger{sink: instance}
	for index := 0; index < 40; index++ {
		logger.Info(context.Background(), "rotating", logging.Int("index", int64(index)))
	}
	if err := instance.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	base := filepath.Join(dir, "logs", "test.log")
	if _, err := os.Stat(base + ".3"); !os.IsNotExist(err) {
		t.Fatalf("third backup exists, want retention capped at 2")
	}
	for _, path := range []string{base, base + ".1", base + ".2"} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if info.Size() > 2*cfg.MaxSizeBytes {
			t.Fatalf("%s size = %d, rotation did not bound it", path, info.Size())
		}
	}
	data, err := os.ReadFile(base)
	if err != nil {
		t.Fatalf("read current file: %v", err)
	}
	if !strings.Contains(string(data), `msg=rotating`) {
		t.Fatalf("current file does not contain the newest records: %q", string(data))
	}
}

func TestWriteErrorsAreRecorded(t *testing.T) {
	t.Parallel()
	instance, _ := captureSink(t, Config{Level: "debug"})
	instance.mu.Lock()
	instance.out = failingWriter{}
	instance.mu.Unlock()
	(&logger{sink: instance}).Info(context.Background(), "record")
	instance.mu.Lock()
	defer instance.mu.Unlock()
	if instance.lastError == nil {
		t.Fatal("write failure was not recorded")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestCloseIsIdempotentAndStopsWriting(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	instance, err := newSink(dir, Config{Output: "file", Path: "logs/test.log"})
	if err != nil {
		t.Fatalf("newSink: %v", err)
	}
	if err := instance.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := instance.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	instance.write(logging.LevelError, "after-close", nil)
	data, err := os.ReadFile(filepath.Join(dir, "logs", "test.log"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(data) != 0 {
		t.Fatalf("record written after Close: %q", string(data))
	}
}

func nonEmptyLines(value string) []string {
	lines := make([]string, 0)
	for _, line := range strings.Split(value, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
