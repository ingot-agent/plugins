package contextcompact

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/ingot-agent/sdk/contextwindow"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/session"
)

// TestCompactSerializesPerSession: concurrent calls for one session must not
// interleave coverage updates. The gate makes them take turns; the assertion is
// that every call returns a valid request and no call fails.
func TestCompactSerializesPerSession(t *testing.T) {
	store := newMemoryStore()
	modelRuntime := &fakeModel{onCall: func(request model.Request) (model.Response, error) {
		return summaryResponse(summaryText("concurrent summary")), nil
	}}
	request := simpleRequest()
	for i := 0; i < 10; i++ {
		request.Messages = append(request.Messages,
			userMessage(fmt.Sprintf("prompt %d %s", i, "pppppppppp")),
			assistantMessage(fmt.Sprintf("answer %d %s", i, "aaaaaaaaaa")),
		)
	}
	c := newCompactor(t, Config{TriggerTokens: 200, TargetTokens: 100}, modelRuntime, store)

	var waitGroup sync.WaitGroup
	errors := make(chan error, 8)
	for i := 0; i < 8; i++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			result, err := c.Compact(context.Background(), contextwindow.CompactionRequest{
				RootSessionID: "s", SessionID: "s", Invocation: request,
			})
			if err != nil {
				errors <- err
				return
			}
			if len(result.Messages) == 0 {
				errors <- fmt.Errorf("empty result")
			}
		}()
	}
	waitGroup.Wait()
	close(errors)
	for err := range errors {
		t.Fatalf("concurrent compact failed: %v", err)
	}
	// Records are append-only, so at least one must exist.
	records := 0
	for _, entry := range mustLoad(t, store, "s") {
		if entry.Kind == recordKind {
			records++
		}
	}
	if records == 0 {
		t.Fatal("no summary record was written")
	}
}

// TestCompactIndependentSessionsDoNotBlock: the gate is per session.
func TestCompactIndependentSessionsDoNotBlock(t *testing.T) {
	store := newMemoryStore()
	c := newCompactor(t, Config{TriggerTokens: 1000, TargetTokens: 500}, &fakeModel{}, store)

	var waitGroup sync.WaitGroup
	errors := make(chan error, 4)
	for i := 0; i < 4; i++ {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			id := session.ID(fmt.Sprintf("s-%d", index))
			if _, err := c.Compact(context.Background(), contextwindow.CompactionRequest{
				RootSessionID: id, SessionID: id, Invocation: simpleRequest("a", "b"),
			}); err != nil {
				errors <- err
			}
		}(i)
	}
	waitGroup.Wait()
	close(errors)
	for err := range errors {
		t.Fatalf("independent session failed: %v", err)
	}
}

// TestCompactRespectsCancellation: a cancelled context is not a compaction
// failure but it must propagate.
func TestCompactRespectsCancellation(t *testing.T) {
	store := newMemoryStore()
	c := newCompactor(t, Config{}, &fakeModel{}, store)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Compact(ctx, contextwindow.CompactionRequest{
		RootSessionID: "s", SessionID: "s", Invocation: simpleRequest("a", "b"),
	}); err == nil {
		t.Fatal("expected a cancellation error")
	}
}
