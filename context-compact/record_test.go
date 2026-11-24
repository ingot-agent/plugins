package contextcompact

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ingot-agent/plugins/context-compact/internal/engine"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/session"
)

func inspect(t *testing.T, request model.Request) engine.Layout {
	t.Helper()
	layout, err := engine.Inspect(request)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	return layout
}

func newRecordTestCompactor(t *testing.T, store session.Store) *compactor {
	t.Helper()
	return newCompactor(t, Config{}, &fakeModel{}, store)
}

// appendEntry writes a raw record, bypassing the growth heuristic.
func appendRecordEntry(t *testing.T, store *memoryStore, value record) {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(context.Background(), "s", session.Entry{
		Kind: recordKind, Version: recordVersion, Payload: payload,
	}); err != nil {
		t.Fatal(err)
	}
}

func makeRecord(t *testing.T, layout engine.Layout, coveredMessages int, summary string) record {
	t.Helper()
	digest, err := conversationDigest(layout.Conversation[:coveredMessages])
	if err != nil {
		t.Fatal(err)
	}
	return record{
		Version: recordVersion, CoveredMessages: coveredMessages, CoveredDigest: digest,
		Summary: summary, Provider: "p", Model: "m",
	}
}

func TestLoadRecordAcceptsMatchingCoverage(t *testing.T) {
	store := newMemoryStore()
	c := newRecordTestCompactor(t, store)
	request := toolRequest(4, 100)
	layout := inspect(t, request)
	boundary := layout.Rounds[1].End

	appendRecordEntry(t, store, makeRecord(t, layout, boundary, "covered summary"))
	got, rounds := c.loadRecord(context.Background(), "s", layout)
	if got.Summary != "covered summary" || rounds != 2 {
		t.Fatalf("record = %#v, rounds = %d", got, rounds)
	}
}

func TestLoadRecordIgnoresStaleDigest(t *testing.T) {
	store := newMemoryStore()
	c := newRecordTestCompactor(t, store)
	request := toolRequest(4, 100)
	layout := inspect(t, request)
	value := makeRecord(t, layout, layout.Rounds[1].End, "summary")
	value.CoveredDigest = strings.Repeat("0", 64)
	appendRecordEntry(t, store, value)

	if got, rounds := c.loadRecord(context.Background(), "s", layout); got.Summary != "" || rounds != 0 {
		t.Fatalf("stale record accepted: %#v rounds=%d", got, rounds)
	}
}

func TestLoadRecordIgnoresNonBoundaryCoverage(t *testing.T) {
	store := newMemoryStore()
	c := newRecordTestCompactor(t, store)
	request := toolRequest(4, 100)
	layout := inspect(t, request)
	// A message index inside a round is not a legal coverage boundary.
	value := makeRecord(t, layout, 2, "summary")
	appendRecordEntry(t, store, value)

	if got, _ := c.loadRecord(context.Background(), "s", layout); got.Summary != "" {
		t.Fatalf("non-boundary record accepted: %#v", got)
	}
}

func TestLoadRecordIgnoresCoverageBeyondHistory(t *testing.T) {
	store := newMemoryStore()
	c := newRecordTestCompactor(t, store)
	request := toolRequest(2, 100)
	layout := inspect(t, request)
	value := makeRecord(t, layout, layout.RoundsEnd(), "summary")
	value.CoveredMessages = layout.RoundsEnd() + 4
	appendRecordEntry(t, store, value)

	if got, _ := c.loadRecord(context.Background(), "s", layout); got.Summary != "" {
		t.Fatalf("out-of-range record accepted: %#v", got)
	}
}

func TestLoadRecordUsesNewestUsableRecord(t *testing.T) {
	store := newMemoryStore()
	c := newRecordTestCompactor(t, store)
	request := toolRequest(4, 100)
	layout := inspect(t, request)

	appendRecordEntry(t, store, makeRecord(t, layout, layout.Rounds[0].End, "old summary"))
	appendRecordEntry(t, store, makeRecord(t, layout, layout.Rounds[2].End, "new summary"))

	got, rounds := c.loadRecord(context.Background(), "s", layout)
	if got.Summary != "new summary" || rounds != 3 {
		t.Fatalf("record = %#v, rounds = %d", got, rounds)
	}
}

func TestLoadRecordSkipsCorruptEntry(t *testing.T) {
	store := newMemoryStore()
	c := newRecordTestCompactor(t, store)
	request := toolRequest(2, 100)
	layout := inspect(t, request)

	if err := store.Append(context.Background(), "s", session.Entry{
		Kind: recordKind, Version: recordVersion, Payload: []byte("not json"),
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ := c.loadRecord(context.Background(), "s", layout); got.Summary != "" {
		t.Fatalf("corrupt record accepted: %#v", got)
	}
}

// TestDigestTracksReplay: a summary must not survive a replay-data change,
// because the covered messages no longer match what a provider can resume.
func TestDigestTracksReplay(t *testing.T) {
	base := []model.Message{
		userMessage("hi"),
		{Role: model.RoleAssistant, Content: text("hello")},
	}
	first, err := conversationDigest(base)
	if err != nil {
		t.Fatal(err)
	}
	withReplay := []model.Message{
		base[0],
		{Role: model.RoleAssistant, Content: text("hello"), Replay: &model.ReplayState{
			Format: "f", Provider: "p", Model: "m", Data: []byte("state"),
		}},
	}
	second, err := conversationDigest(withReplay)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("digest ignored Replay data")
	}
	again, err := conversationDigest(withReplay)
	if err != nil {
		t.Fatal(err)
	}
	if second != again {
		t.Fatal("digest is not deterministic")
	}
}

func TestShouldSkipRecord(t *testing.T) {
	previous := record{CoveredMessages: 10, Summary: strings.Repeat("s", 1000)}
	for name, test := range map[string]struct {
		next record
		skip bool
	}{
		"no coverage growth":   {next: record{CoveredMessages: 10, Summary: strings.Repeat("s", 2000)}, skip: true},
		"coverage shrank":      {next: record{CoveredMessages: 9, Summary: strings.Repeat("s", 2000)}, skip: true},
		"tiny summary growth":  {next: record{CoveredMessages: 20, Summary: strings.Repeat("s", 1050)}, skip: true},
		"large summary growth": {next: record{CoveredMessages: 20, Summary: strings.Repeat("s", 2000)}, skip: false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := shouldSkipRecord(previous, test.next); got != test.skip {
				t.Fatalf("skip = %v, want %v", got, test.skip)
			}
		})
	}
	if shouldSkipRecord(record{}, record{CoveredMessages: 5, Summary: "first"}) {
		t.Fatal("the first record must always be written")
	}
}

func TestAppendRecordSurvivesStoreFailure(t *testing.T) {
	store := newMemoryStore()
	store.err = context.DeadlineExceeded
	c := newRecordTestCompactor(t, store)
	// Must not panic or block; the failure is logged only.
	c.appendRecord(context.Background(), "s", record{CoveredMessages: 4, Summary: "x"}, record{})
}

func TestSummaryPreambleRendersNotes(t *testing.T) {
	messages := summaryPreamble(record{Summary: "the story", Notes: "- constraint"})
	if len(messages) != 1 {
		t.Fatalf("messages = %d", len(messages))
	}
	body := engine.TextOf(messages[0])
	for _, want := range []string{"the story", "- constraint", "does not override system instructions"} {
		if !strings.Contains(body, want) {
			t.Fatalf("preamble missing %q: %s", want, body)
		}
	}
	if got := summaryPreamble(record{}); got != nil {
		t.Fatalf("empty record must render nothing: %#v", got)
	}
}
