package appcomponent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	appbackend "github.com/ingot-agent/plugins/app-webui"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/workspace"
)

func waitTurnStatus(t *testing.T, registry *turnRegistry, id, status string) appbackend.TurnFinished {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	state, err := registry.agent.State(ctx)
	for err == nil {
		for _, snapshot := range projectSubmissions(state) {
			if snapshot.ID == id && snapshot.Status == status {
				return appbackend.TurnFinished{TurnSnapshot: snapshot, InvocationID: id, HistoryEnd: snapshot.HistoryEnd, Outcome: snapshot.Outcome, Result: snapshot.Result, Error: snapshot.Error}
			}
		}
		_, err = registry.agent.Watch(ctx, state.Revision)
		if err == nil {
			state, err = registry.agent.State(ctx)
		}
	}
	t.Fatalf("wait %s: %v", status, err)
	return appbackend.TurnFinished{}
}

func TestWebSubmissionsShareTurnAndKeepInputOrder(t *testing.T) {
	app := testApplication(t)
	started, release := make(chan agent.Turn, 4), make(chan struct{}, 4)
	runtime := &testAgent{run: func(ctx context.Context, turn agent.Turn) (agent.Execution, error) {
		started <- turn
		select {
		case <-release:
		case <-ctx.Done():
			return agent.Execution{}, ctx.Err()
		}
		return agent.Execution{Result: &agent.Result{Output: content.FromText("reply: " + turn.Inputs[0].Text)}}, nil
	}}
	replaceTestController(app, testController(t, runtime))
	send := func(text string) appbackend.TurnSnapshot {
		t.Helper()
		s, err := app.turns.Submit(context.Background(), agent.MessageRequest{SessionID: "s", RequestID: text, Input: agent.Input{Text: text}})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	a := send("A")
	<-started
	b, c := send("B"), send("C")
	if duplicate := send("B"); duplicate.ID != b.ID {
		t.Fatal("duplicate submission")
	}
	release <- struct{}{}
	select {
	case turn := <-started:
		if turn.Inputs[0].Text != "C" {
			t.Fatalf("next prompt input=%+v", turn)
		}
	case <-time.After(time.Second):
		t.Fatal("no next round")
	}
	during := app.turns.Snapshots()
	if len(during) != 3 || during[0].SDKTurnID == "" || during[0].SDKTurnID != during[1].SDKTurnID || during[1].SDKTurnID != during[2].SDKTurnID {
		t.Fatalf("snapshots=%+v", during)
	}
	if during[0].Output != "reply: A" || during[1].Output != "" {
		t.Fatalf("output order=%+v", during)
	}
	release <- struct{}{}
	final := waitTurnStatus(t, app.turns, c.ID, "succeeded")
	if final.Output != "reply: C" || final.HistoryEnd == nil || *final.HistoryEnd != 5 {
		t.Fatalf("final=%+v", final)
	}
	if waitTurnStatus(t, app.turns, a.ID, "succeeded").SDKTurnID != final.SDKTurnID {
		t.Fatal("split lifecycle")
	}
}

func TestHTTPMessageRetryPauseAndAttachmentFailure(t *testing.T) {
	app := testApplication(t)
	created, err := app.sessions.Create(context.Background(), "input", workspace.Binding{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.turns.SetPaused(session.ID(created.ID), true); err != nil {
		t.Fatal(err)
	}
	body := `{"sessionId":"` + created.ID + `","requestId":"same","input":"file","attachments":[{"kind":"file","path":"` + strings.ReplaceAll(t.TempDir()+"/missing.txt", "\\", "/") + `","name":"missing.txt"}]}`
	send := func() appbackend.TurnSnapshot {
		t.Helper()
		w := httptest.NewRecorder()
		app.routes().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/turns", strings.NewReader(body)))
		if w.Code != http.StatusAccepted {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		var s appbackend.TurnSnapshot
		if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	first, second := send(), send()
	if first.ID != second.ID || first.Status != "queued" {
		t.Fatal("retry/pause failed")
	}
	if _, err := app.turns.SetPaused(session.ID(created.ID), false); err != nil {
		t.Fatal(err)
	}
	failed := waitTurnStatus(t, app.turns, first.ID, "failed")
	if failed.Error == nil || failed.Persisted {
		t.Fatalf("failure=%+v", failed)
	}
	if !app.turns.QueueSnapshots()[0].Paused {
		t.Fatal("failure pause cleared")
	}
}
