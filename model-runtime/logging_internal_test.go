package modelruntime

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ingot-agent/sdk/logging"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/observation"
)

// recordSink collects the records one test produced without touching global
// logging state.
type recordSink struct {
	mu      sync.Mutex
	records []recorded
	attrs   []logging.Attr
}

type recorded struct {
	level   logging.Level
	message string
	attrs   []logging.Attr
}

func (s *recordSink) Enabled(level logging.Level) bool { return true }

func (s *recordSink) Debug(ctx context.Context, message string, attrs ...logging.Attr) {
	s.emit(ctx, logging.LevelDebug, message, attrs)
}

func (s *recordSink) Info(ctx context.Context, message string, attrs ...logging.Attr) {
	s.emit(ctx, logging.LevelInfo, message, attrs)
}

func (s *recordSink) Warn(ctx context.Context, message string, attrs ...logging.Attr) {
	s.emit(ctx, logging.LevelWarn, message, attrs)
}

func (s *recordSink) Error(ctx context.Context, message string, attrs ...logging.Attr) {
	s.emit(ctx, logging.LevelError, message, attrs)
}

func (s *recordSink) With(attrs ...logging.Attr) logging.Logger {
	retained := append(append([]logging.Attr(nil), s.attrs...), attrs...)
	return &derivedSink{parent: s, attrs: retained}
}

func (s *recordSink) emit(_ context.Context, level logging.Level, message string, attrs []logging.Attr) {
	s.mu.Lock()
	defer s.mu.Unlock()
	combined := append(append([]logging.Attr(nil), s.attrs...), attrs...)
	s.records = append(s.records, recorded{level: level, message: message, attrs: combined})
}

func (s *recordSink) snapshot() []recorded {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]recorded(nil), s.records...)
}

type derivedSink struct {
	parent *recordSink
	attrs  []logging.Attr
}

func (d *derivedSink) Enabled(level logging.Level) bool { return d.parent.Enabled(level) }
func (d *derivedSink) Debug(ctx context.Context, message string, attrs ...logging.Attr) {
	d.parent.emit(ctx, logging.LevelDebug, message, attrs)
}
func (d *derivedSink) Info(ctx context.Context, message string, attrs ...logging.Attr) {
	d.parent.emit(ctx, logging.LevelInfo, message, attrs)
}
func (d *derivedSink) Warn(ctx context.Context, message string, attrs ...logging.Attr) {
	d.parent.emit(ctx, logging.LevelWarn, message, attrs)
}
func (d *derivedSink) Error(ctx context.Context, message string, attrs ...logging.Attr) {
	d.parent.emit(ctx, logging.LevelError, message, attrs)
}
func (d *derivedSink) With(attrs ...logging.Attr) logging.Logger {
	return &derivedSink{parent: d.parent, attrs: append(append([]logging.Attr(nil), d.attrs...), attrs...)}
}

var _ logging.Logger = (*recordSink)(nil)

func attrText(attrs []logging.Attr, key string) (string, bool) {
	for _, attr := range attrs {
		if attr.Key != key {
			continue
		}
		switch attr.Value.Kind {
		case logging.KindString:
			return attr.Value.String, true
		case logging.KindInt64:
			return "", true
		case logging.KindBool:
			return "", true
		}
	}
	return "", false
}

func TestProviderRetryIsReportedThroughInjectedLogger(t *testing.T) {
	t.Parallel()
	sink := &recordSink{}
	instance := &runtime{logger: sink}
	ctx := observation.WithCorrelation(context.Background(), observation.Correlation{SessionID: "session-1", TurnID: "turn-1"})
	instance.logProviderRetry(ctx, model.Request{Provider: "provider-1", Model: "model-1"}, 2, 5, 1500*time.Millisecond, true)
	records := sink.snapshot()
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	record := records[0]
	if record.level != logging.LevelWarn || record.message != "retrying model provider request" {
		t.Fatalf("record = %+v", record)
	}
	for key, want := range map[string]string{
		"provider": "provider-1",
		"model":    "model-1",
	} {
		if got, _ := attrText(record.attrs, key); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
	values := map[string]int64{}
	for _, attr := range record.attrs {
		if attr.Value.Kind == logging.KindInt64 {
			values[attr.Key] = attr.Value.Int64
		}
	}
	if values["attempt"] != 2 || values["max_attempts"] != 5 || values["delay_ms"] != 1500 {
		t.Fatalf("numeric attributes = %+v", values)
	}
}

func TestAbsentLoggerRecordsNothingAndDoesNotPanic(t *testing.T) {
	t.Parallel()
	instance := &runtime{logger: logging.Discard()}
	instance.logProviderRetry(context.Background(), model.Request{Provider: "p", Model: "m"}, 2, 3, time.Second, false)
	if instance.logger.Enabled(logging.LevelError) {
		t.Fatal("Discard enabled a level")
	}
}
