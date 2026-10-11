package agentdefault

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/asset"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/pipeline"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/tool"
)

type admissionSessions struct {
	session.Manager
	metadata session.Metadata
	err      error
}

func (s admissionSessions) Get(context.Context, session.ID) (session.Metadata, error) {
	return s.metadata, s.err
}

func TestSubmissionRejectsMissingAndArchivedBeforeQueueing(t *testing.T) {
	exports, _ := newMessageRuntime(t, Config{}, panicModel{}, &fakeTools{})
	r := exports.Runtime.(*runtime)
	now := time.Now()
	for _, tc := range []struct {
		sessions admissionSessions
		want     error
	}{
		{admissionSessions{err: session.ErrNotFound}, session.ErrNotFound},
		{admissionSessions{metadata: session.Metadata{ID: "s", ArchivedAt: &now}}, session.ErrArchived},
	} {
		r.sessions = tc.sessions
		if err := r.SendMessage(context.Background(), agent.MessageRequest{SessionID: "s", Input: agent.Input{Text: "A"}}); !errors.Is(err, tc.want) {
			t.Fatalf("admission = %v", err)
		}
		state, _ := r.State(context.Background())
		if len(state.Messages) != 0 || len(state.Queues) != 0 {
			t.Fatalf("rejected input queued: %+v", state)
		}
	}
}

func TestInitialAttachmentFailureHasNoLifecycleAndRetainsHistoryAnchor(t *testing.T) {
	exports, _ := newMessageRuntime(t, Config{}, panicModel{}, &fakeTools{})
	err := exports.Runtime.SendMessage(context.Background(), agent.MessageRequest{SessionID: "s", Input: agent.Input{Attachments: []agent.InputAttachment{{Path: filepath.Join(t.TempDir(), "missing")}}}})
	if err != nil {
		t.Fatal(err)
	}
	state := waitMessageState(t, exports.Control, idleState)
	if len(state.Turns) != 0 || state.Messages[0].Status != "failed" || !state.Queues[0].Paused || state.Messages[0].HistoryEnd == nil || *state.Messages[0].HistoryEnd != 0 {
		t.Fatalf("initial failure = %+v", state)
	}
}

func TestConcurrentSubmissionsAtFinalizationDoNotLoseWakeups(t *testing.T) {
	finishing, release := make(chan struct{}), make(chan struct{})
	var hooks, active, calls atomic.Int32
	models := completeModelFunc(func(_ context.Context, _ model.Request) (model.Response, error) {
		if active.Add(1) != 1 {
			t.Error("concurrent model execution for one session")
		}
		defer active.Add(-1)
		calls.Add(1)
		return model.Response{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("done")}}, nil
	})
	hook := agentInterceptorFunc(func(ctx context.Context, turn agent.Turn, next pipeline.Next[agent.Turn, agent.Result]) (agent.Result, error) {
		result, err := next(ctx, turn)
		if hooks.Add(1) == 1 {
			close(finishing)
			select {
			case <-release:
			case <-ctx.Done():
				return agent.Result{}, ctx.Err()
			}
		}
		return result, err
	})
	exports, _ := newMessageRuntime(t, Config{MaxRounds: 1}, models, &fakeTools{}, hook)
	if err := exports.Runtime.SendMessage(context.Background(), agent.MessageRequest{SessionID: "s", Input: agent.Input{Text: "A"}}); err != nil {
		t.Fatal(err)
	}
	<-finishing // The last boundary is already past; new input must start another lifecycle.
	var senders sync.WaitGroup
	for i := range 32 {
		senders.Add(1)
		go func() {
			defer senders.Done()
			<-release
			if err := exports.Runtime.SendMessage(context.Background(), agent.MessageRequest{SessionID: "s", Input: agent.Input{Text: fmt.Sprint(i)}}); err != nil {
				t.Error(err)
			}
		}()
	}
	close(release)
	senders.Wait()
	state := waitMessageState(t, exports.Control, func(s agent.RuntimeState) bool { return idleState(s) && len(s.Turns) == 33 })
	if calls.Load() != 33 {
		t.Fatalf("calls = %d", calls.Load())
	}
	for _, item := range state.Messages {
		if item.Status != "succeeded" {
			t.Fatalf("stranded submission: %+v", item)
		}
	}
}

type blockingAttachmentStore struct {
	asset.Store
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingAttachmentStore) Stat(ctx context.Context, ref asset.Reference) (asset.Info, error) {
	s.once.Do(func() { close(s.entered) })
	select {
	case <-s.release:
		return s.Store.Stat(ctx, ref)
	case <-ctx.Done():
		return asset.Info{}, ctx.Err()
	}
}

func TestBoundarySnapshotDoesNotConsumeLaterArrivals(t *testing.T) {
	modelEntered, modelRelease := make(chan struct{}), make(chan struct{})
	requests := make(chan model.Request, 4)
	var calls atomic.Int32
	models := completeModelFunc(func(ctx context.Context, request model.Request) (model.Response, error) {
		requests <- request
		if calls.Add(1) == 1 {
			close(modelEntered)
			select {
			case <-modelRelease:
			case <-ctx.Done():
				return model.Response{}, ctx.Err()
			}
		}
		return model.Response{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("done")}}, nil
	})
	exports, _ := newMessageRuntime(t, Config{MaxRounds: 4}, models, &fakeTools{})
	r := exports.Runtime.(*runtime)
	reference, _, err := r.assets.Put(context.Background(), asset.PutRequest{Body: bytes.NewReader([]byte{1}), Size: 1})
	if err != nil {
		t.Fatal(err)
	}
	assets := &blockingAttachmentStore{Store: r.assets, entered: make(chan struct{}), release: make(chan struct{})}
	r.assets = assets
	send := func(text string, attachments ...agent.InputAttachment) {
		t.Helper()
		if err := exports.Runtime.SendMessage(context.Background(), agent.MessageRequest{SessionID: "s", Input: agent.Input{Text: text, Attachments: attachments}}); err != nil {
			t.Fatal(err)
		}
	}
	send("A")
	<-modelEntered
	send("B", agent.InputAttachment{Media: &content.Attachment{Kind: content.KindImage, Media: content.Media{MIMEType: "image/png", Source: content.Source{Kind: content.SourceAsset, Asset: reference}}}})
	send("C")
	close(modelRelease)
	select {
	case <-assets.entered:
	case <-time.After(time.Second):
		t.Fatal("boundary preparation not entered")
	}
	send("D")
	close(assets.release)
	state := waitMessageState(t, exports.Control, idleState)
	if calls.Load() != 3 || len(state.Turns) != 1 {
		t.Fatalf("calls=%d turns=%d", calls.Load(), len(state.Turns))
	}
	<-requests
	second, third := <-requests, <-requests
	if textValue(second.Messages[len(second.Messages)-1].Content) != "C" || textValue(third.Messages[len(third.Messages)-1].Content) != "D" {
		t.Fatalf("snapshot absorbed late input: second=%+v third=%+v", second, third)
	}
}

func TestStopCancelsWholeTurnAndKeepsPendingPaused(t *testing.T) {
	entered := make(chan struct{})
	models := completeModelFunc(func(ctx context.Context, _ model.Request) (model.Response, error) {
		close(entered)
		<-ctx.Done()
		return model.Response{}, ctx.Err()
	})
	exports, _ := newMessageRuntime(t, Config{}, models, &fakeTools{})
	if err := exports.Runtime.SendMessage(context.Background(), agent.MessageRequest{SessionID: "s", Input: agent.Input{Text: "A"}}); err != nil {
		t.Fatal(err)
	}
	<-entered
	if err := exports.Runtime.SendMessage(context.Background(), agent.MessageRequest{SessionID: "s", Input: agent.Input{Text: "B"}}); err != nil {
		t.Fatal(err)
	}
	if err := exports.Control.Stop(context.Background(), "s"); err != nil {
		t.Fatal(err)
	}
	state := waitMessageState(t, exports.Control, idleState)
	if state.Messages[0].Status != "canceled" || state.Messages[1].Status != "queued" || !state.Queues[0].Paused || state.Turns[0].Execution.Outcome.Status != agent.OutcomeCanceled {
		t.Fatalf("state=%+v", state)
	}
}

func TestCanceledPendingRetentionAndStickyPause(t *testing.T) {
	exports, _ := newMessageRuntime(t, Config{}, &sequenceModel{}, &fakeTools{})
	if err := exports.Control.SetPaused(context.Background(), "s", true); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < messageRetention+10; i++ {
		if err := exports.Runtime.SendMessage(context.Background(), agent.MessageRequest{SessionID: "s", Input: agent.Input{Text: "unused"}}); err != nil {
			t.Fatal(err)
		}
		state, _ := exports.Control.State(context.Background())
		if err := exports.Control.CancelMessage(context.Background(), state.Messages[len(state.Messages)-1].ID, true); err != nil {
			t.Fatal(err)
		}
	}
	state, _ := exports.Control.State(context.Background())
	if len(state.Messages) > messageRetention || !state.Queues[0].Paused || len(state.Turns) != 0 {
		t.Fatalf("state=%+v", state)
	}
}

func waitMessageState(t *testing.T, control agent.Control, ready func(agent.RuntimeState) bool) agent.RuntimeState {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	state, err := control.State(ctx)
	for err == nil && !ready(state) {
		_, err = control.Watch(ctx, state.Revision)
		if err == nil {
			state, err = control.State(ctx)
		}
	}
	if err != nil {
		t.Fatalf("wait runtime state: %v; state=%+v", err, state)
	}
	return state
}

func idleState(state agent.RuntimeState) bool {
	if len(state.Messages) == 0 {
		return false
	}
	for _, q := range state.Queues {
		if q.ActiveID != "" {
			return false
		}
	}
	return true
}

func newMessageRuntime(t *testing.T, cfg Config, models model.Runtime, tools tool.Runtime, interceptors ...agent.Interceptor) (Exports, *memoryStore) {
	t.Helper()
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	exports, cleanup, err := New(context.Background(), withState(t, cfg, Dependencies{Model: models, Tools: tools, Store: store, Assets: newMemoryAssets(), Prompt: passthroughPrompt{}, Interceptors: interceptors}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := cleanup(ctx); err != nil {
			t.Error(err)
		}
	})
	return exports, store
}

func TestSendMessageAdmitsAllPendingAfterCompleteToolRound(t *testing.T) {
	toolEntered, releaseTool := make(chan struct{}), make(chan struct{})
	requests := make(chan model.Request, 4)
	var calls, hooks atomic.Int32
	models := completeModelFunc(func(_ context.Context, request model.Request) (model.Response, error) {
		requests <- cloneModelRequest(request)
		if calls.Add(1) == 1 {
			return model.Response{Message: model.Message{Role: model.RoleAssistant, ToolCalls: []tool.Call{{ID: "a-call", Name: "a", Arguments: json.RawMessage(`{}`)}}}}, nil
		}
		return model.Response{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("final")}}, nil
	})
	tools := &toolRuntimeFunc{call: func(ctx context.Context, _ tool.Call) (tool.Result, error) {
		close(toolEntered)
		select {
		case <-releaseTool:
			return tool.Result{Content: content.FromText("tool done")}, nil
		case <-ctx.Done():
			return tool.Result{}, ctx.Err()
		}
	}}
	hook := agentInterceptorFunc(func(ctx context.Context, turn agent.Turn, next pipeline.Next[agent.Turn, agent.Result]) (agent.Result, error) {
		hooks.Add(1)
		return next(ctx, turn)
	})
	exports, store := newMessageRuntime(t, Config{MaxRounds: 3}, models, tools, hook)
	ctx, cancel := context.WithCancel(context.Background())
	if err := exports.Runtime.SendMessage(ctx, agent.MessageRequest{SessionID: "s", RequestID: "A", Input: agent.Input{Text: "A"}}); err != nil {
		t.Fatal(err)
	}
	cancel() // admission cancellation must not kill accepted work
	select {
	case <-toolEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("tool did not start")
	}
	first := <-requests
	if got := textValue(first.Messages[len(first.Messages)-1].Content); got != "A" {
		t.Fatal(got)
	}
	file := filepath.Join(t.TempDir(), "notes.md")
	if err := os.WriteFile(file, []byte("notes"), 0600); err != nil {
		t.Fatal(err)
	}
	b := agent.MessageRequest{SessionID: "s", RequestID: "B", Input: agent.Input{Text: "B", Attachments: append([]agent.InputAttachment{{Path: file, Name: "notes.md"}}, agent.MediaAttachments([]content.Attachment{{Kind: content.KindImage, Media: content.Inline(content.KindImage, "image/png", "new.png", []byte{1, 2, 3}).Media}})...)}}
	if err := exports.Runtime.SendMessage(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if err := exports.Runtime.SendMessage(context.Background(), b); err != nil {
		t.Fatal(err)
	} // dedup
	b.Input.Text = "different"
	if err := exports.Runtime.SendMessage(context.Background(), b); !errors.Is(err, agent.ErrRequestConflict) {
		t.Fatal(err)
	}
	if err := exports.Runtime.SendMessage(context.Background(), agent.MessageRequest{SessionID: "s", RequestID: "C", Input: agent.Input{Text: "C"}}); err != nil {
		t.Fatal(err)
	}
	state, _ := exports.Control.State(context.Background())
	if len(state.Messages) != 3 || state.Messages[1].Status != "queued" || calls.Load() != 1 {
		t.Fatalf("state=%+v calls=%d", state, calls.Load())
	}
	entries, _ := store.Load(context.Background(), "s")
	if len(entries) != 2 {
		t.Fatalf("new input persisted inside tool group: %d", len(entries))
	}
	close(releaseTool)
	state = waitMessageState(t, exports.Control, idleState)
	second := <-requests
	var roles []model.Role
	var text []string
	for _, m := range second.Messages {
		roles = append(roles, m.Role)
		text = append(text, textValue(m.Content))
	}
	want := []model.Role{model.RoleUser, model.RoleAssistant, model.RoleTool, model.RoleUser, model.RoleUser, model.RoleUser}
	if !reflect.DeepEqual(roles, want) || second.Messages[4].Content[0].Text != "B" || text[5] != "C" || !strings.Contains(text[3], "notes.md") {
		t.Fatalf("roles=%v text=%v", roles, text)
	}
	if len(second.Messages[4].Content) != 2 || second.Messages[4].Content[1].Kind != content.KindImage || second.Messages[4].Content[1].Media.Source.Kind != content.SourceAsset {
		t.Fatalf("round-boundary image missing: %+v", second.Messages[4])
	}
	if calls.Load() != 2 || hooks.Load() != 1 || len(state.Turns) != 1 || len(state.Turns[0].Turn.Inputs) != 3 {
		t.Fatalf("calls=%d hooks=%d state=%+v", calls.Load(), hooks.Load(), state)
	}
	for _, m := range state.Messages {
		if m.TurnID != state.Turns[0].TurnID || m.Status != "succeeded" || !m.Persisted {
			t.Fatalf("submission=%+v", m)
		}
	}
	if state.Turns[0].Execution.Outcome.Status != agent.OutcomeSucceeded || len(state.Turns[0].Blocks) != 5 {
		t.Fatalf("turn=%+v", state.Turns[0])
	}
}

func TestAttachmentFailurePausesRemainderAndKeepsSuccessfulPrefix(t *testing.T) {
	for _, prefix := range []bool{false, true} {
		t.Run(map[bool]string{false: "first-fails", true: "prefix-succeeds"}[prefix], func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			models := completeModelFunc(func(ctx context.Context, request model.Request) (model.Response, error) {
				if calls.Add(1) == 1 {
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
						return model.Response{}, ctx.Err()
					}
				}
				return model.Response{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("answer")}}, nil
			})
			exports, store := newMessageRuntime(t, Config{MaxRounds: 3}, models, &fakeTools{})
			send := func(id string, input agent.Input) {
				t.Helper()
				if err := exports.Runtime.SendMessage(context.Background(), agent.MessageRequest{SessionID: "s", RequestID: id, Input: input}); err != nil {
					t.Fatal(err)
				}
			}
			send("A", agent.Input{Text: "A"})
			<-entered
			if prefix {
				send("B", agent.Input{Text: "B"})
			}
			send("bad", agent.Input{Text: "bad", Attachments: []agent.InputAttachment{{Path: filepath.Join(t.TempDir(), "deleted.md")}}})
			send("D", agent.Input{Text: "D"})
			close(release)
			state := waitMessageState(t, exports.Control, idleState)
			if !state.Queues[0].Paused || state.Queues[0].PauseReason != "attachment" || state.Turns[0].Status != "succeeded" {
				t.Fatalf("state=%+v", state)
			}
			wantCalls := int32(1)
			if prefix {
				wantCalls = 2
			}
			if calls.Load() != wantCalls {
				t.Fatal(calls.Load())
			}
			for _, m := range state.Messages {
				if m.Request.RequestID == "bad" && (m.Status != "failed" || m.Persisted || m.Error == "") {
					t.Fatalf("failure=%+v", m)
				}
				if m.Request.RequestID == "D" && m.Status != "queued" {
					t.Fatal(m)
				}
			}
			entries, _ := store.Load(context.Background(), "s")
			for _, e := range entries {
				if strings.Contains(string(e.Payload), "deleted.md") || strings.Contains(string(e.Payload), `"text":"bad"`) {
					t.Fatalf("failed input persisted: %s", e.Payload)
				}
			}
			if err := exports.Control.SetPaused(context.Background(), "s", false); err != nil {
				t.Fatal(err)
			}
			state = waitMessageState(t, exports.Control, func(s agent.RuntimeState) bool {
				for _, m := range s.Messages {
					if m.Request.RequestID == "D" {
						return m.Status == "succeeded"
					}
				}
				return false
			})
			for _, m := range state.Messages {
				if m.Request.RequestID == "bad" && m.Status != "failed" {
					t.Fatal("failed item retried")
				}
			}
		})
	}
}

func TestRoundBudgetLeavesPendingForNextTurn(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	models := completeModelFunc(func(ctx context.Context, _ model.Request) (model.Response, error) {
		if calls.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return model.Response{}, ctx.Err()
			}
		}
		return model.Response{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("done")}}, nil
	})
	exports, _ := newMessageRuntime(t, Config{MaxRounds: 1}, models, &fakeTools{})
	_ = exports.Runtime.SendMessage(context.Background(), agent.MessageRequest{SessionID: "s", Input: agent.Input{Text: "A"}})
	<-entered
	_ = exports.Runtime.SendMessage(context.Background(), agent.MessageRequest{SessionID: "s", Input: agent.Input{Text: "B"}})
	close(release)
	state := waitMessageState(t, exports.Control, func(s agent.RuntimeState) bool { return idleState(s) && len(s.Turns) == 2 })
	if state.Messages[0].TurnID == state.Messages[1].TurnID || calls.Load() != 2 {
		t.Fatalf("state=%+v", state)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := exports.Runtime.SendMessage(ctx, agent.MessageRequest{SessionID: "s", Input: agent.Input{Text: "canceled"}}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
