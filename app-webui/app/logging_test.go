package appcomponent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	appbackend "github.com/ingot-agent/plugins/app-webui"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/logging"
	"github.com/ingot-agent/sdk/session"
)

// recordSink captures records without touching global logging state.
type recordSink struct {
	mu      sync.Mutex
	levels  []logging.Level
	records []logging.Attr
}

func (s *recordSink) Enabled(logging.Level) bool { return true }
func (s *recordSink) Debug(_ context.Context, _ string, attrs ...logging.Attr) {
	s.emit(logging.LevelDebug)
}
func (s *recordSink) Info(_ context.Context, _ string, attrs ...logging.Attr) {
	s.emit(logging.LevelInfo)
}
func (s *recordSink) Error(_ context.Context, _ string, attrs ...logging.Attr) {
	s.emit(logging.LevelError)
}

func (s *recordSink) Warn(_ context.Context, _ string, attrs ...logging.Attr) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.levels = append(s.levels, logging.LevelWarn)
	s.records = append(s.records, attrs...)
}

func (s *recordSink) With(attrs ...logging.Attr) logging.Logger { return s }

func (s *recordSink) emit(level logging.Level) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.levels = append(s.levels, level)
}

func (s *recordSink) sessionAttribute() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, attr := range s.records {
		if attr.Key == "session" && attr.Value.Kind == logging.KindString {
			return attr.Value.String, true
		}
	}
	return "", false
}

var _ logging.Logger = (*recordSink)(nil)

// failingChannel fails every Clear so the best-effort cleanup enters its error
// path. The embedded nil imports the remaining Channel methods; calling them is
// a test bug.
type failingChannel struct{ interaction.Channel }

func (failingChannel) Clear(context.Context, string) error { return errors.New("unavailable") }

// failingInteractionHost is the smallest InteractionHost whose Scoped channel
// fails; unrelated methods are never called by clearSessionUsage.
type failingInteractionHost struct{ appbackend.InteractionHost }

func (failingInteractionHost) Scoped(appbackend.Scope) interaction.Channel { return failingChannel{} }

type loggingBackend struct {
	interactions appbackend.InteractionHost
}

func (b loggingBackend) Events() appbackend.EventHub              { return nil }
func (b loggingBackend) Interactions() appbackend.InteractionHost { return b.interactions }

func TestClearSessionUsageReportsThroughInjectedLogger(t *testing.T) {
	t.Parallel()
	sink := &recordSink{}
	instance := &application{backend: loggingBackend{interactions: failingInteractionHost{}}, logger: sink}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	instance.clearSessionUsage(ctx, session.ID("session-1"))
	if len(sink.levels) == 0 {
		t.Fatal("cleanup failure was not reported")
	}
	for _, level := range sink.levels {
		if level != logging.LevelWarn {
			t.Fatalf("level = %v, want warning", level)
		}
	}
	if value, ok := sink.sessionAttribute(); !ok || value != "session-1" {
		t.Fatalf("session attribute = %q, present=%v", value, ok)
	}
	// Two prefixes are cleared, so both failures are reported independently.
	if len(sink.levels) != 2 {
		t.Fatalf("records = %d, want 2", len(sink.levels))
	}
}

func TestClearSessionUsageWithoutLoggerDoesNotPanic(t *testing.T) {
	t.Parallel()
	instance := &application{backend: loggingBackend{interactions: failingInteractionHost{}}, logger: logging.Discard()}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	instance.clearSessionUsage(ctx, session.ID("session-1"))
}
