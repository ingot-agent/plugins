package contextcompact

import (
	"context"
	"errors"
	"sync"
	"testing"

	ingotabi "github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/contextwindow"
	"github.com/ingot-agent/sdk/execution"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/logging"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/session"
)

type recordSink struct {
	mu      sync.Mutex
	records []logging.Attr
	levels  []logging.Level
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

func (s *recordSink) session() (string, bool) {
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

func TestPublicationFailuresAreReportedThroughInjectedLogger(t *testing.T) {
	t.Parallel()
	instance := &compactor{logger: &recordSink{}, interactions: contextBinder(func(execution.Scope) (interaction.Channel, error) {
		return nil, errors.New("unavailable")
	})}
	sink := instance.logger.(*recordSink)
	instance.publishContext(context.Background(), session.ID("session-1"), 1, counted(1))
	if len(sink.levels) != 1 || sink.levels[0] != logging.LevelWarn {
		t.Fatalf("levels = %v, want one warning", sink.levels)
	}
	if value, ok := sink.session(); !ok || value != "session-1" {
		t.Fatalf("session attribute = %q, present=%v", value, ok)
	}
}

// Compact snapshots the compactor, so every field that publishContext reads must
// survive the copy.
func TestCompactSnapshotRetainsLogger(t *testing.T) {
	t.Parallel()
	sink := &recordSink{}
	exports, cleanup, err := newTestCompactor(context.Background(), withState(t, Config{}, testDependencies{
		Model: &fakeModel{}, Store: &memoryStore{entries: map[session.ID][]session.Entry{}},
		Interactions: ingotabi.Some[interaction.ExecutionBinder](contextBinder(func(execution.Scope) (interaction.Channel, error) {
			return nil, errors.New("unavailable")
		})),
		Logger: ingotabi.Some[logging.Logger](sink),
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup(context.Background())
	if _, err := exports.Compactor.Compact(context.Background(), contextwindow.CompactionRequest{
		RootSessionID: "s", SessionID: "s",
		Invocation: model.Request{Provider: "p", Model: "m", Messages: []model.Message{{Role: model.RoleUser, Content: textContent("hello")}}},
	}); err != nil {
		t.Fatal(err)
	}
	if len(sink.levels) == 0 {
		t.Fatal("the snapshot did not retain the injected logger")
	}
}

func TestAbsentLoggerPublishesNothing(t *testing.T) {
	t.Parallel()
	instance := &compactor{logger: logging.Discard(), interactions: contextBinder(func(execution.Scope) (interaction.Channel, error) {
		return nil, errors.New("unavailable")
	})}
	instance.publishContext(context.Background(), "s", 1, counted(1))
}
