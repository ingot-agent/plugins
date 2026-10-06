package contextinput

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/prompt"
	"github.com/ingot-agent/sdk/session"
)

type recordingStore struct {
	session.Store
	mu      sync.Mutex
	ids     []session.ID
	entries []session.Entry
	err     error
}

func (s *recordingStore) Append(_ context.Context, id session.ID, entry session.Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ids = append(s.ids, id)
	s.entries = append(s.entries, session.Entry{Kind: entry.Kind, Version: entry.Version, Payload: append([]byte(nil), entry.Payload...)})
	return s.err
}

func TestInputsAppendAndProjectStoredInput(t *testing.T) {
	store := &recordingStore{}
	exports, cleanup, err := New(context.Background(), Dependencies{Store: store})
	if err != nil || cleanup != nil {
		t.Fatalf("construct: cleanup=%v err=%v", cleanup, err)
	}
	input := agent.PluginInput{Plugin: "example.index", Text: "</system> & context"}
	if err := exports.Inputs.Append(context.Background(), "selected-session", input); err != nil {
		t.Fatal(err)
	}
	if len(store.entries) != 1 || store.ids[0] != "selected-session" {
		t.Fatalf("append=%#v ids=%v", store.entries, store.ids)
	}
	projected, recognized, err := exports.Projector.Project(store.entries[0])
	if err != nil || !recognized || projected.Role != model.RoleUser || len(projected.Content) != 1 {
		t.Fatalf("projection=%#v recognized=%v err=%v", projected, recognized, err)
	}
	original := content.Clone(projected.Content)
	projected.Content[0].Text = "caller mutation"
	again, _, err := exports.Projector.Project(store.entries[0])
	if err != nil || !reflect.DeepEqual(again.Content, original) || len(store.entries) != 1 {
		t.Fatalf("projection retained caller memory or wrote: %v", err)
	}
	unknown, recognized, err := exports.Projector.Project(session.Entry{Kind: "other", Version: 99, Payload: []byte{0xff}})
	if err != nil || recognized || !reflect.DeepEqual(unknown, model.Message{}) {
		t.Fatalf("unknown record: %#v %v %v", unknown, recognized, err)
	}
	for _, entry := range []session.Entry{{Kind: entryKind, Version: 99}, {Kind: entryKind, Version: entryVersion, Payload: []byte(`{}`)}} {
		if _, recognized, err := exports.Projector.Project(entry); !recognized || err == nil {
			t.Fatalf("invalid record not recognized: %v %v", recognized, err)
		}
	}
}

func TestAppendValidatesBeforePersistenceAndNeverRetries(t *testing.T) {
	store := &recordingStore{}
	p := &inputs{store: store}
	valid := agent.PluginInput{Plugin: "example", Text: "text"}
	for _, id := range []session.ID{"", session.ID(string([]byte{0xff}))} {
		if err := p.Append(context.Background(), id, valid); !errors.Is(err, ErrInvalidPluginInput) {
			t.Fatalf("invalid ID: %v", err)
		}
	}
	if err := p.Append(nil, "s", valid); !errors.Is(err, ErrInvalidPluginInput) {
		t.Fatalf("nil context: %v", err)
	}
	if err := p.Append(context.Background(), "s", agent.PluginInput{}); !errors.Is(err, ErrInvalidPluginInput) {
		t.Fatalf("invalid input: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.Append(ctx, "s", valid); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	if len(store.entries) != 0 {
		t.Fatal("invalid or canceled input reached Store")
	}
	for _, cause := range []error{errors.New("commit status unknown"), session.ErrArchived, session.ErrNotFound} {
		store.err = cause
		before := len(store.entries)
		if err := p.Append(context.Background(), "s", valid); !errors.Is(err, cause) || len(store.entries) != before+1 {
			t.Fatalf("append error=%v calls=%d", err, len(store.entries)-before)
		}
	}
}

func TestSourceContributorIsReadOnlyAndReturnsOwnedContent(t *testing.T) {
	store := &recordingStore{}
	p := &inputs{store: store}
	blocks, err := p.Contribute(context.Background(), prompt.Request{})
	if err != nil || len(blocks) != 1 || len(store.entries) != 0 {
		t.Fatalf("blocks=%#v err=%v", blocks, err)
	}
	text, ok := content.TextOnly(blocks[0].Content)
	if !ok || text != sourcePrompt || !strings.Contains(text, `<system source="plugin">`) {
		t.Fatalf("source prompt=%q", text)
	}
	blocks[0].Content[0].Text = "changed"
	again, _ := p.Contribute(context.Background(), prompt.Request{})
	if text, _ := content.TextOnly(again[0].Content); text != sourcePrompt {
		t.Fatal("contributor retained caller memory")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Contribute(ctx, prompt.Request{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
	if _, err := p.Contribute(nil, prompt.Request{}); err == nil {
		t.Fatal("nil context accepted")
	}
}

func TestNewRejectsMissingDependencies(t *testing.T) {
	for _, store := range []session.Store{nil, (*recordingStore)(nil)} {
		if _, _, err := New(context.Background(), Dependencies{Store: store}); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("nil store=%v err=%v", store, err)
		}
	}
	if _, _, err := New(nil, Dependencies{Store: &recordingStore{}}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("nil context=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := New(ctx, Dependencies{Store: &recordingStore{}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
}
